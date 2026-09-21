"""Real Go fixture and failure-injection tests for the required Windows runner."""
import contextlib
from collections import Counter
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location(
    "windows_shards", Path(__file__).with_name("windows-shards.py")
)
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


class NativeFixtureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="dev-windows-shard-test-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.root = Path(cls.temporary.name)
        cls.previous_cwd = Path.cwd()
        os.chdir(cls.root)
        cls.addClassCleanup(os.chdir, cls.previous_cwd)
        (cls.root / "go.mod").write_text(
            "module example.test/windows-shards\n\ngo 1.22.0\n", encoding="utf-8"
        )
        for package in ("internal/cli", "internal/taskflow", "util", "cmd/demo"):
            directory = cls.root / package
            directory.mkdir(parents=True)
            name = directory.name
            (directory / "source.go").write_text(
                "package " + ("main" if name == "demo" else name) + "\n"
                + ("func main() {}\n" if name == "demo" else ""), encoding="utf-8"
            )
        (cls.root / "internal/cli/source_test.go").write_text('''package cli
import ("fmt"; "os"; "testing")
func TestMain(m *testing.M) { os.Exit(m.Run()) }
func TestAlpha(t *testing.T) { t.Parallel() }
func TestBeta(t *testing.T) { t.Run("nested/sub", func(t *testing.T) { t.Log("subtest executed") }) }
func TestSkipped(t *testing.T) { t.Skip("explicit fixture boundary") }
func FuzzValue(f *testing.F) { f.Add(1); f.Fuzz(func(t *testing.T, x int) { t.Log("fuzz seed executed") }) }
func Example() { fmt.Println("example")
// Output: example
}
func BenchmarkNotRequested(b *testing.B) { b.Fatal("benchmarks are not ordinary tests") }
''', encoding="utf-8")
        (cls.root / "internal/taskflow/source_test.go").write_text(
            'package taskflow\nimport "testing"\nfunc TestTask(t *testing.T) {}\n', encoding="utf-8"
        )
        cls.utility = cls.root / "util/source_test.go"
        cls.utility.write_text(
            'package util\nimport "testing"\nfunc TestUtility(t *testing.T) {}\n', encoding="utf-8"
        )
        git_env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)
        for args in (["init", "-q"], ["add", "."], ["-c", "user.name=Fixture", "-c",
                     "user.email=fixture@example.test", "commit", "-qm", "fixture"]):
            subprocess.run(["git", *args], env=git_env, check=True, capture_output=True)
        cls.baseline = cls.root / "baseline"
        with contextlib.redirect_stdout(io.StringIO()) as log:
            cls.plan = RUNNER.discover(3)
            for shard in range(3):
                if RUNNER.run_shard(cls.plan, shard, cls.baseline) != 0:
                    raise AssertionError("real fixture failed:\n" + log.getvalue())
            RUNNER.audit(cls.plan, cls.baseline)

    def setUp(self):
        self.result_dir = self.root / "copy"
        shutil.copytree(self.baseline, self.result_dir, dirs_exist_ok=True)
        self.addCleanup(shutil.rmtree, self.result_dir)

    def assert_audit_fails(self):
        with self.assertRaises((ValueError, FileNotFoundError)):
            RUNNER.audit(self.plan, self.result_dir)

    def filtered_result(self):
        task = next(task for task in self.plan["tasks"] if task["roots"])
        path = self.result_dir / ("shard-%02d" % task["shard"]) / "result.json"
        report = json.loads(path.read_text(encoding="utf-8"))
        result = next(result for result in report["results"] if result["task"] == task)
        return path, report, result

    def test_native_examples_fuzz_subtests_and_package_inventory(self):
        cli = "example.test/windows-shards/internal/cli"
        self.assertEqual(self.plan["split"][cli]["roots"],
                         ["Example", "FuzzValue", "TestAlpha", "TestBeta", "TestSkipped"])
        self.assertEqual(self.plan["split"][cli]["benchmarks_not_run_by_default"],
                         ["BenchmarkNotRequested"])
        self.assertEqual(len(self.plan["packages"]), 4)
        logs = "\n".join(path.read_text(encoding="utf-8")
                         for path in self.result_dir.glob("shard-*/*.jsonl"))
        self.assertIn("subtest executed", logs)
        self.assertIn("fuzz seed executed", logs)
        self.assertIn("explicit fixture boundary", logs)
        self.assertIn("[no test files]", logs)
        with contextlib.redirect_stdout(io.StringIO()):
            RUNNER.audit(self.plan, self.result_dir)

    def test_missing_or_duplicate_shard_rejected(self):
        report = self.result_dir / "shard-00/result.json"
        original = report.read_text(encoding="utf-8")
        report.unlink()
        self.assert_audit_fails()
        report.write_text(original, encoding="utf-8")
        duplicate = self.result_dir / "shard-extra"
        duplicate.mkdir()
        (duplicate / "result.json").write_text(original, encoding="utf-8")
        self.assert_audit_fails()

    def test_missing_duplicate_and_unfinished_tasks_rejected(self):
        path, report, _ = self.filtered_result()
        for mutate in (
            lambda value: value.update(completed=False),
            lambda value: value["results"].pop(),
            lambda value: value["results"].append(copy.deepcopy(value["results"][0])),
            lambda value: value.update(plan_hash="stale"),
        ):
            changed = copy.deepcopy(report)
            mutate(changed)
            RUNNER.dump(path, changed)
            self.assert_audit_fails()
        RUNNER.dump(path, report)

    def test_missing_root_or_completion_cannot_be_hidden_by_ledger(self):
        path, report, result = self.filtered_result()
        result["observed_roots"] = {}
        RUNNER.dump(path, report)
        self.assert_audit_fails()
        # Even if the report claims success, altered/incomplete native logs fail.
        shutil.copytree(self.baseline, self.result_dir, dirs_exist_ok=True)
        path, report, result = self.filtered_result()
        log = path.parent / result["log"]
        original = log.read_text(encoding="utf-8")
        for action in ("run", "pass"):
            events = [json.loads(line) for line in original.splitlines()]
            events = [event for event in events if not (
                event.get("Test") in result["task"]["roots"] and event.get("Action") == action
            )]
            log.write_text("\n".join(json.dumps(event) for event in events), encoding="utf-8")
            self.assert_audit_fails()
        log.unlink()
        self.assert_audit_fails()

    def test_failure_propagates_and_later_tasks_still_execute(self):
        original = self.utility.read_text(encoding="utf-8")
        self.addCleanup(self.utility.write_text, original, encoding="utf-8")
        self.utility.write_text(
            'package util\nimport "testing"\n'
            'func TestUtility(t *testing.T) { t.Fatal("intentional fixture failure") }\n',
            encoding="utf-8"
        )
        task = next(task for task in self.plan["tasks"] if task["package"].endswith("/util"))
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(RUNNER.run_shard(self.plan, task["shard"], self.result_dir), 1)
        path = self.result_dir / ("shard-%02d" % task["shard"]) / "result.json"
        report = json.loads(path.read_text(encoding="utf-8"))
        self.assertTrue(report["completed"])
        self.assertEqual(len(report["results"]), sum(
            task["shard"] == item["shard"] for item in self.plan["tasks"]
        ))
        self.assertFalse(report["results"][0]["okay"])
        self.assertTrue(any(item["okay"] for item in report["results"][1:]))
        self.assert_audit_fails()

    def test_go_timeout_is_a_failure(self):
        original = self.utility.read_text(encoding="utf-8")
        self.addCleanup(self.utility.write_text, original, encoding="utf-8")
        self.utility.write_text(
            'package util\nimport ("testing"; "time")\n'
            'func TestUtility(t *testing.T) { time.Sleep(time.Second) }\n', encoding="utf-8"
        )
        task = next(task for task in self.plan["tasks"] if task["package"].endswith("/util"))
        with mock.patch.object(RUNNER, "TEST_TIMEOUT", "50ms"), contextlib.redirect_stdout(io.StringIO()):
            result = RUNNER.run_task(task, self.result_dir / "shard-00")
        self.assertFalse(result["okay"])
        self.assertNotEqual(result["exit_code"], 0)

    def test_outer_process_timeout_is_a_failure(self):
        task = next(task for task in self.plan["tasks"] if task["package"].endswith("/util"))
        # An invocation cannot report success after its process deadline, even
        # if it was still compiling rather than running the test binary.
        with mock.patch.object(RUNNER, "PROCESS_TIMEOUT", 0.001), contextlib.redirect_stdout(io.StringIO()):
            result = RUNNER.run_task(task, self.result_dir / "shard-00")
        self.assertTrue(result["outer_timeout"])
        self.assertFalse(result["okay"])

    def test_stale_execution_identity_rejected(self):
        changed = copy.deepcopy(self.plan)
        changed["identity"]["commit"] = "stale"
        with self.assertRaisesRegex(ValueError, "differ"):
            RUNNER.run_shard(changed, 0, self.result_dir)
        with self.assertRaisesRegex(ValueError, "differs"):
            RUNNER.audit(changed, self.result_dir)


class PlanTests(unittest.TestCase):
    def test_command_budget_splits_without_losing_any_roots(self):
        package = "m/internal/cli"
        roots = sorted("Test" + str(index) + "界" * 350 for index in range(180))
        tasks = RUNNER.make_tasks([package], {package: {"roots": roots}}, 3)
        self.assertGreater(len(tasks), 3)
        self.assertEqual(Counter(root for task in tasks for root in task["roots"]), Counter(roots))
        for task in tasks:
            self.assertLessEqual(RUNNER.command_units(RUNNER.command(task)), RUNNER.MAX_COMMAND)
            regex = RUNNER.root_regex(task["roots"])
            self.assertTrue(regex.startswith("^(") and regex.endswith(")$"))
            self.assertNotIn("/", regex)
        with self.assertRaises(ValueError):
            RUNNER.make_tasks([package], {package: {"roots": ["Test" + "A" * 20000]}}, 1)

    def test_empty_split_and_other_packages_execute_once(self):
        packages = ["m/cli", "m/other", "m/taskflow"]
        split = {"m/cli": {"roots": []}, "m/taskflow": {"roots": ["TestA", "TestB"]}}
        tasks = RUNNER.make_tasks(packages, split, 4)
        self.assertEqual([task["package"] for task in tasks if task["roots"] is None],
                         ["m/cli", "m/other"])
        plan = {"schema": RUNNER.SCHEMA, "shards": 4, "packages": packages,
                "split": split, "tasks": tasks}
        RUNNER.validate_plan(plan)
        plan["tasks"].pop()
        with self.assertRaisesRegex(ValueError, "complete deterministic inventory"):
            RUNNER.validate_plan(plan)

    def test_duplicate_discovery_and_invalid_regex_roots_fail_closed(self):
        event = {"Package": "m/cli", "Action": "output", "Output": "TestA\nTestA\n"}
        with self.assertRaisesRegex(ValueError, "duplicate"):
            RUNNER.roots_from_list(json.dumps(event), "m/cli")
        for names in ([], ["TestA/Child"], ["TestA|.*"]):
            with self.assertRaises(ValueError):
                RUNNER.root_regex(names)


if __name__ == "__main__":
    unittest.main()

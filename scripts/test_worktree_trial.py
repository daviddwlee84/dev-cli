"""External trial roots and cleanup fixtures must not touch user repositories."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


SOURCE = Path(__file__).resolve().parents[1] / "contrib/worktree-trial"
SPEC = importlib.util.spec_from_file_location("worktree_trial", SOURCE / "trial.py")
trial = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(trial)


class WorktreeTrialTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="trial-unit-", dir=Path(tempfile.gettempdir()).resolve())
        self.addCleanup(self.temporary.cleanup)
        self.parent = Path(self.temporary.name)
        self.root = self.parent / "new-pilot"

    def test_dry_run_has_no_writes_and_existing_root_is_refused(self):
        result = trial.create(str(self.root), dry_run=True)
        self.assertTrue(result["dry_run"])
        self.assertFalse(self.root.exists())
        self.root.mkdir()
        with self.assertRaisesRegex(trial.TrialError, "already exists"):
            trial.create(str(self.root))
        keep = self.root / "user-data.txt"
        keep.write_text("keep me")
        with self.assertRaises(trial.TrialError):
            trial.create(str(self.root))
        self.assertEqual(keep.read_text(), "keep me")

    def test_relative_escape_and_missing_parent_are_refused(self):
        for path in ("relative/pilot", str(self.parent / ".." / "escape"), str(self.parent / "missing" / "pilot")):
            with self.subTest(path=path), self.assertRaises(trial.TrialError):
                trial.create(path, dry_run=True)

    def test_symlink_ancestors_and_dangling_root_are_refused(self):
        target = self.parent / "target"
        target.mkdir()
        alias = self.parent / "alias"
        try:
            alias.symlink_to(target, target_is_directory=True)
        except OSError:
            self.skipTest("symlink creation unavailable")
        with self.assertRaisesRegex(trial.TrialError, "symlink"):
            trial.create(str(alias / "pilot"))
        self.root.symlink_to(self.parent / "missing")
        with self.assertRaisesRegex(trial.TrialError, "symlink"):
            trial.create(str(self.root))
        self.assertEqual(list(target.iterdir()), [])

    def test_checkout_and_bare_repository_parents_are_refused(self):
        for name, bare in (("checkout", False), ("bare.git", True)):
            path = self.parent / name
            trial.git(self.parent, "init", *( ["--bare"] if bare else []), str(path))
            with self.subTest(bare=bare), self.assertRaisesRegex(trial.TrialError, "Git"):
                trial.create(str(path / "pilot"), dry_run=True)

    def test_clones_share_seed_without_tasks_or_preimplemented_features(self):
        with mock.patch.dict(os.environ, {"GIT_DIR": "/do-not-use", "GIT_WORK_TREE": "/do-not-use", "DEV_CONFIG": "/do-not-use"}):
            manifest = trial.create(str(self.root))
        self.assertEqual(manifest["manual_evaluation"], "not_run")
        self.assertTrue(trial.verify_manifest(trial.Tree(self.root)))
        for path in map(Path, manifest["routes"].values()):
            self.assertEqual(trial.git(path, "rev-parse", "HEAD").stdout.strip(), manifest["seed_commit"])
            self.assertEqual(trial.git(path, "status", "--porcelain").stdout, "")
            self.assertEqual(trial.git(path, "remote", "get-url", "origin").stdout.strip(), manifest["origin"])
        feedback = json.loads((self.root / "feedback.json").read_text())
        self.assertIsNone(feedback["routes"]["A"]["tasks"]["json"]["acceptance_passed"])
        self.assertEqual(json.loads((self.root / "sidecar/config.json").read_text()), {})
        self.assertEqual(manifest["sidecar"]["tmux_tmpdir"], str(self.root / "sidecar/tmux"))
        self.assertEqual(list((self.root / "sidecar/tmux").iterdir()), [])
        self.assertEqual(list((self.root / "dev/state").iterdir()), [])
        env = trial.command_env()
        env.setdefault("GOTOOLCHAIN", "local")
        tested = subprocess.run(["go", "test", "./..."], cwd=self.root / "A-herdr-dev", env=env,
                                capture_output=True, text=True, timeout=120)
        self.assertEqual(tested.returncode, 0, tested.stdout + tested.stderr)
        for task in ("json", "unicode"):
            checked = subprocess.run([os.sys.executable, str(self.root / "acceptance.py"), "--task", task,
                                      "--repo", str(self.root / "A-herdr-dev")], env=env,
                                     capture_output=True, text=True, timeout=120)
            self.assertEqual(checked.returncode, 4, checked.stdout + checked.stderr)
            self.assertFalse(json.loads(checked.stdout)["passed"])

    def test_manifest_mismatch_blocks_cleanup_authority(self):
        trial.create(str(self.root))
        tree = trial.Tree(self.root)
        path = self.root / trial.MANIFEST
        manifest = json.loads(path.read_text())
        manifest["root"] = str(self.parent)
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(trial.TrialError, "manifest"):
            trial.verify_manifest(tree)

    def test_help_and_dry_run_are_machine_usable(self):
        help_result = subprocess.run([os.sys.executable, str(SOURCE / "trial.py"), "--help"], capture_output=True, text=True)
        self.assertEqual(help_result.returncode, 0)
        self.assertIn("Exit:", help_result.stdout)
        result = subprocess.run([os.sys.executable, str(SOURCE / "trial.py"), "create", "--root", str(self.root), "--dry-run"],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(result.stdout)["dry_run"])
        self.assertFalse(self.root.exists())

    @unittest.skipUnless(os.environ.get("WORKTREE_TRIAL_DEV"), "set WORKTREE_TRIAL_DEV to a current built dev binary")
    def test_public_cleanup_fixtures(self):
        command = subprocess.run([os.sys.executable, str(SOURCE / "trial.py"), "check", "--dev",
                                  os.environ["WORKTREE_TRIAL_DEV"]], capture_output=True, text=True, timeout=120)
        self.assertEqual(command.returncode, 0, command.stdout + command.stderr)
        result = json.loads(command.stdout)
        self.assertTrue(result["passed"], json.dumps(result, indent=2))
        self.assertTrue(result["no_dev_tasks"])
        self.assertTrue(result["temporary_fixture_removed"])
        self.assertEqual(len(result["cases"]), 6)
        self.assertIn("merged_branch_deleted", {case["case"] for case in result["cases"]})


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Run every native package and CLI/taskflow root; reject incomplete coverage.

Go's -list and -run include tests, examples and fuzz seeds. Root-only patterns
leave subtest selection to Go; benchmarks remain off just as in go test ./....
Split batches repeat package initialization/TestMain, but omit no listed roots.
"""
import argparse
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time

SCHEMA = 1
MAX_COMMAND = 16000  # UTF-16 units, conservatively below CreateProcessW's 32767.
TEST_TIMEOUT = "20m"
PROCESS_TIMEOUT = 1500  # The test-binary limit plus cold compilation allowance.


def output(args):
    return subprocess.check_output(args, text=True, encoding="utf-8").strip()


def dump(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def identity():
    meta = json.loads(output(["go", "env", "-json", "GOOS", "GOARCH", "GOVERSION", "CGO_ENABLED", "GOFLAGS"]))
    native = {"win32": "windows", "darwin": "darwin", "linux": "linux"}.get(sys.platform)
    if meta["GOOS"] != native:
        raise ValueError("cross-compilation is not native test execution")
    if meta["GOFLAGS"]:
        raise ValueError("GOFLAGS must be empty; hidden build/test selectors are not allowed")
    meta["commit"] = output(["git", "rev-parse", "HEAD"])
    return meta


def roots_from_list(stdout, package):
    names, benchmarks = [], []
    for line in stdout.splitlines():
        event = json.loads(line)
        if event.get("Package") != package or event.get("Action") != "output":
            continue
        for name in event.get("Output", "").splitlines():
            if re.fullmatch(r"(?:Test|Example|Fuzz)\w*", name):
                names.append(name)
            elif re.fullmatch(r"Benchmark\w*", name):
                benchmarks.append(name)
    if len(names) != len(set(names)):
        raise ValueError("duplicate discovered root tests: " + package)
    return sorted(names), sorted(set(benchmarks))


def root_regex(names):
    if not names or any(not re.fullmatch(r"(?:Test|Example|Fuzz)\w*", n) for n in names):
        raise ValueError("a filtered batch must contain valid, nonempty Go root names")
    # These are function identifiers; explicitly quote regexp metacharacters.
    quote = lambda n: re.sub(r"([\\.+*?()|\[\]{}^$])", r"\\\1", n)
    return "^(" + "|".join(quote(n) for n in names) + ")$"


def command(task):
    args = ["go", "test", "-json", "-count=1", "-timeout=" + TEST_TIMEOUT]
    if task["roots"] is not None:
        args += ["-run", root_regex(task["roots"])]
    return args + [task["package"]]


def command_units(args):
    return len(subprocess.list2cmdline(args).encode("utf-16-le")) // 2 + 1


def make_tasks(packages, split, count):
    tasks = []
    other = [p for p in packages if p not in split or not split[p]["roots"]]
    for index, package in enumerate(other):
        tasks.append({"shard": index % count, "package": package, "roots": None})
    for package in sorted(split):
        roots = split[package]["roots"]
        for shard in range(count):
            selected = roots[shard::count]
            batch = []
            for name in selected:
                trial = {"shard": shard, "package": package, "roots": batch + [name]}
                if command_units(command(trial)) > MAX_COMMAND:
                    if not batch:
                        raise ValueError("one root exceeds Windows command budget: " + name)
                    tasks.append({"shard": shard, "package": package, "roots": batch})
                    batch = []
                batch.append(name)
                if command_units(command({"shard": shard, "package": package, "roots": batch})) > MAX_COMMAND:
                    raise ValueError("root name exceeds Windows command budget")
            if batch:
                tasks.append({"shard": shard, "package": package, "roots": batch})
    for index, task in enumerate(tasks):
        task["id"] = "task-%04d" % index
        if command_units(command(task)) > MAX_COMMAND:
            raise ValueError("package command exceeds Windows command budget")
    return tasks


def validate_plan(plan):
    if plan.get("schema") != SCHEMA or not 1 <= plan["shards"] <= 16:
        raise ValueError("unsupported plan")
    packages, split = plan["packages"], plan["split"]
    if not packages or packages != sorted(set(packages)) or not set(split) <= set(packages):
        raise ValueError("invalid package inventory")
    for package, listing in split.items():
        if listing["roots"] != sorted(set(listing["roots"])):
            raise ValueError("invalid root inventory: " + package)
    expected = make_tasks(packages, split, plan["shards"])
    if plan["tasks"] != expected:
        raise ValueError("tasks differ from the complete deterministic inventory")
    for package in packages:
        tasks = [t for t in expected if t["package"] == package]
        roots = split.get(package, {}).get("roots", [])
        if roots:
            assigned = Counter(n for t in tasks for n in t["roots"])
            if assigned != Counter(roots):
                raise ValueError("missing/duplicate split roots: " + package)
        elif len(tasks) != 1 or tasks[0]["roots"] is not None:
            raise ValueError("whole package must run exactly once: " + package)


def plan_hash(plan):
    return hashlib.sha256(json.dumps(plan, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def discover(count):
    meta = identity()
    packages = sorted(output(["go", "list", "./..."]).splitlines())
    split_paths = output(["go", "list", "./internal/cli", "./internal/taskflow"]).splitlines()
    if len(split_paths) != 2 or len(set(split_paths)) != 2:
        raise ValueError("both CLI and taskflow packages must be discovered")
    split = {}
    for package in split_paths:
        print("DISCOVER", package, flush=True)
        result = subprocess.run(["go", "test", "-json", "-list", ".", package], capture_output=True, text=True, encoding="utf-8", timeout=900)
        if result.returncode:
            print(result.stdout, result.stderr, file=sys.stderr)
            raise ValueError("native test discovery failed: " + package)
        roots, benchmarks = roots_from_list(result.stdout, package)
        split[package] = {"roots": roots, "benchmarks_not_run_by_default": benchmarks}
    plan = {"schema": SCHEMA, "shards": count, "identity": meta, "packages": packages, "split": split, "tasks": make_tasks(packages, split, count)}
    validate_plan(plan)
    return plan


def observe_log(log, task):
    started, completed, failures = Counter(), Counter(), []
    package_terminal = False
    for line in log.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue  # Go build diagnostics are not necessarily JSON.
        if event.get("Package") != task["package"]:
            continue
        name, action = event.get("Test"), event.get("Action")
        if name and "/" not in name:
            if action == "run":
                started[name] += 1
            elif action in ("pass", "skip"):
                completed[name] += 1
        if not name and action in ("pass", "skip"):
            package_terminal = True
        if action == "fail":
            failures.append(name or task["package"])
    expected = started if task["roots"] is None else Counter(task["roots"])
    return {
        "observed_roots": dict(started),
        "completed_roots": dict(completed),
        "coverage": started == expected and completed == expected,
        "package_terminal": package_terminal,
        "failures": failures,
    }


def stop_process_tree(process):
    if sys.platform == "win32":
        subprocess.run(
            ["taskkill", "/PID", str(process.pid), "/T", "/F"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30,
        )
    else:
        os.killpg(process.pid, signal.SIGKILL)
    return process.wait(timeout=30)


def run_task(task, directory):
    args = command(task)
    if command_units(args) > MAX_COMMAND:
        raise ValueError("Windows command length invariant failed")
    started = time.monotonic()
    log = directory / (task["id"] + ".jsonl")
    print("RUN", task["id"], task["package"], "all roots" if task["roots"] is None else len(task["roots"]), flush=True)
    with log.open("w", encoding="utf-8") as stream:
        process = subprocess.Popen(
            args, stdout=stream, stderr=subprocess.STDOUT, text=True,
            encoding="utf-8", start_new_session=sys.platform != "win32",
        )
        timed_out = False
        try:
            code = process.wait(timeout=PROCESS_TIMEOUT)
        except subprocess.TimeoutExpired:
            timed_out = True
            code = stop_process_tree(process)
    observations = observe_log(log, task)
    okay = (code == 0 and not timed_out and observations["package_terminal"]
            and observations["coverage"] and not observations["failures"])
    result = {
        "task": task, "okay": okay, "exit_code": code,
        "outer_timeout": timed_out, "seconds": round(time.monotonic() - started, 3),
        **observations, "log": log.name,
    }
    print("PASS" if okay else "FAIL", task["id"], "%.1fs" % result["seconds"], flush=True)
    if not okay:
        if not observations["coverage"]:
            print("Root coverage mismatch", json.dumps({"expected": task["roots"], **observations}), flush=True)
        # Keep complete logs as artifacts; print a bounded useful tail as well.
        print("\n".join(log.read_text(encoding="utf-8", errors="replace").splitlines()[-100:]), flush=True)
    return result


def run_shard(plan, shard, output_dir):
    validate_plan(plan)
    if not 0 <= shard < plan["shards"]:
        raise ValueError("invalid shard number")
    if identity() != plan["identity"]:
        raise ValueError("commit/toolchain/platform/flags differ from native discovery")
    directory = output_dir / ("shard-%02d" % shard)
    directory.mkdir(parents=True, exist_ok=True)
    report = {"schema": SCHEMA, "shard": shard, "plan_hash": plan_hash(plan), "identity": plan["identity"], "results": [], "completed": False}
    path = directory / "result.json"
    dump(path, report)
    for task in plan["tasks"]:
        if task["shard"] != shard:
            continue
        try:
            result = run_task(task, directory)
        except Exception as exc:
            result = {"task": task, "okay": False, "error": str(exc)}
            print("FAIL", task["id"], str(exc), flush=True)
        report["results"].append(result)
        dump(path, report)
    report["completed"] = True
    dump(path, report)
    return 0 if all(r["okay"] for r in report["results"]) else 1


def audit(plan, results):
    validate_plan(plan)
    if output(["git", "rev-parse", "HEAD"]) != plan["identity"]["commit"]:
        raise ValueError("audit checkout differs from the discovered commit")
    paths = sorted(results.glob("shard-*/result.json"))
    reports = [json.loads(p.read_text(encoding="utf-8")) for p in paths]
    if Counter(r["shard"] for r in reports) != Counter(range(plan["shards"])):
        raise ValueError("missing or duplicate shard results")
    seen = []
    for path, report in zip(paths, reports):
        if report["schema"] != SCHEMA or not report["completed"] or report["plan_hash"] != plan_hash(plan) or report["identity"] != plan["identity"]:
            raise ValueError("incomplete, stale, or foreign shard results")
        for result in report["results"]:
            task = result["task"]
            if task["shard"] != report["shard"]:
                raise ValueError("task executed by the wrong shard")
            if (not result["okay"] or result.get("exit_code") != 0
                    or result.get("outer_timeout") is not False):
                raise ValueError("test invocation failed or did not finish: " + task["id"])
            # Re-read the complete native output, rather than trusting a ledger's
            # summary. An absent log or a root that only started is incomplete.
            expected_log = task["id"] + ".jsonl"
            if result.get("log") != expected_log:
                raise ValueError("unexpected test log path")
            observations = observe_log(path.parent / expected_log, task)
            if (not observations["coverage"] or not observations["package_terminal"]
                    or observations["failures"]):
                raise ValueError("test log does not prove successful complete coverage")
            if any(result.get(key) != value for key, value in observations.items()):
                raise ValueError("test ledger differs from its native output")
            seen.append(task)
    if sorted(seen, key=lambda t: int(t["id"].removeprefix("task-"))) != plan["tasks"]:
        raise ValueError("missing, duplicate, or altered test tasks")
    print(json.dumps({"status": "complete", "commit": plan["identity"]["commit"], "packages": len(plan["packages"]), "shards": plan["shards"], "split_roots": {p: len(v["roots"]) for p, v in plan["split"].items()}, "tasks": len(seen)}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    p = sub.add_parser("plan")
    p.add_argument("--shards", type=int, default=8)
    p.add_argument("--out", type=Path, required=True)
    p.add_argument("--expected-os", default="windows")
    p = sub.add_parser("run")
    p.add_argument("--plan", type=Path, required=True)
    p.add_argument("--shard", type=int, required=True)
    p.add_argument("--out", type=Path, required=True)
    p = sub.add_parser("audit")
    p.add_argument("--plan", type=Path, required=True)
    p.add_argument("--results", type=Path, required=True)
    args = parser.parse_args()
    if args.action == "plan":
        if not 1 <= args.shards <= 16:
            parser.error("--shards must be between 1 and 16")
        plan = discover(args.shards)
        if plan["identity"]["GOOS"] != args.expected_os:
            parser.error("native platform does not match --expected-os")
        dump(args.out, plan)
        print(json.dumps({"packages": len(plan["packages"]), "roots": {p: len(v["roots"]) for p, v in plan["split"].items()}, "tasks": len(plan["tasks"])}))
        if os.environ.get("GITHUB_OUTPUT"):
            with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as out:
                out.write("matrix=" + json.dumps({"shard": list(range(args.shards))}) + "\n")
                out.write("go_version=" + plan["identity"]["GOVERSION"].removeprefix("go") + "\n")
        return 0
    plan = json.loads(args.plan.read_text(encoding="utf-8"))
    if args.action == "run":
        return run_shard(plan, args.shard, args.out)
    audit(plan, args.results)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print("ERROR:", exc, file=sys.stderr)
        raise SystemExit(1)

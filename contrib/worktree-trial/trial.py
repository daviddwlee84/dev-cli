#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Create an external worktree pilot, or check cleanup with disposable fixtures.

No command launches an agent, TUI, or real runtime. stdout is one JSON object.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import uuid


HERE = Path(__file__).resolve().parent
MANIFEST = "worktree-trial.json"
HISTORY = ".specstory/history/synthetic-trial-session.md"


class TrialError(Exception):
    def __init__(self, message, code=4):
        super().__init__(message)
        self.code = code


def command_env(home=None):
    # Local fixture operations never inherit Git routing, hooks, identities,
    # runtime socket targets, or the user's dev configuration override.
    env = {key: value for key, value in os.environ.items()
           if not key.startswith(("GIT_", "HERDR_", "DEV_", "TMUX", "ZELLIJ"))}
    env.pop("GOROOT", None)
    env.update(LC_ALL="C", GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
               GIT_TERMINAL_PROMPT="0", GIT_AUTHOR_NAME="Worktree trial",
               GIT_AUTHOR_EMAIL="trial@example.invalid", GIT_COMMITTER_NAME="Worktree trial",
               GIT_COMMITTER_EMAIL="trial@example.invalid", GIT_AUTHOR_DATE="2026-01-01T00:00:00Z",
               GIT_COMMITTER_DATE="2026-01-01T00:00:00Z", GOWORK="off", GOPROXY="off")
    if home is not None:
        env.update(HOME=str(home), USERPROFILE=str(home),
                   XDG_CONFIG_HOME=str(home / "config"), XDG_DATA_HOME=str(home / "data"),
                   XDG_STATE_HOME=str(home / "state"), XDG_CACHE_HOME=str(home / "cache"))
    return env


def run(args, cwd, env=None, check=True):
    try:
        result = subprocess.run([str(arg) for arg in args], cwd=cwd, env=env or command_env(),
                                stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise TrialError(f"could not run {args[0]}: {error}; verify the required local executable", 3) from error
    if check and result.returncode:
        raise TrialError(f"{args[0]} failed (exit {result.returncode}): {result.stderr.strip()[-2000:]}; "
                         "inspect the retained fixture before retrying", 3)
    return result


def git(cwd, *args, env=None, check=True):
    return run(["git", "-c", "core.hooksPath=" + os.devnull, "-c", "commit.gpgSign=false",
                "-c", "tag.gpgSign=false", *args], cwd, env, check)


def inspect_path(path):
    """Reject lexical escapes and every existing symlink component, including parents."""
    if not path.is_absolute() or ".." in path.parts:
        raise TrialError("--root must be an absolute path without '..'; choose a new external directory", 2)
    current = Path(path.anchor)
    for component in path.parts[1:]:
        current = current / component
        try:
            info = current.lstat()
        except FileNotFoundError:
            break
        if stat.S_ISLNK(info.st_mode):
            raise TrialError(f"symlink traversal refused at {current}; use the physical parent path")
        if current != path and not stat.S_ISDIR(info.st_mode):
            raise TrialError(f"parent {current} is not a directory; choose an existing physical parent")


def validate_destination(raw):
    root = Path(raw)
    inspect_path(root)
    if os.path.lexists(root):
        raise TrialError(f"destination already exists: {root}; choose a new name (even an empty directory is refused)")
    if not root.parent.is_dir():
        raise TrialError(f"parent directory is missing: {root.parent}; create the external parent explicitly first", 2)
    for parent in (root.parent, *root.parent.parents):
        if os.path.lexists(parent / ".git"):
            raise TrialError(f"destination is inside a Git checkout: {parent}; choose an external root")
    probe = git(root.parent, "rev-parse", "--git-dir", check=False)
    if probe.returncode == 0:
        raise TrialError("destination is inside a Git repository (including a bare repository); choose an external root")
    if "not a git repository" not in probe.stderr.lower():
        raise TrialError("Git could not prove the parent is outside a repository; repair Git/path access before retrying")
    return root


class Tree:
    """An exclusively created root; every subsequent write stays within its identity."""
    def __init__(self, root):
        self.root = root
        self.identity = self._identity()

    def _identity(self):
        info = self.root.lstat()
        if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):
            raise TrialError("generated root is no longer an ordinary directory; stop and inspect it")
        return {"device": info.st_dev, "inode": info.st_ino}

    def check(self):
        inspect_path(self.root)
        if self._identity() != self.identity:
            raise TrialError("generated root identity changed; refusing further operations")

    def path(self, relative):
        self.check()
        relative = Path(relative)
        if relative.is_absolute() or ".." in relative.parts:
            raise TrialError("internal fixture path escaped its generated root")
        path = self.root / relative
        inspect_path(path)
        return path

    def mkdir(self, relative):
        path = self.path(relative)
        path.mkdir(mode=0o700, parents=True, exist_ok=False)
        return path

    def write(self, relative, content):
        path = self.path(relative)
        path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        inspect_path(path)
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
        try:
            fd = os.open(path, flags, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
                handle.write(content)
        except FileExistsError as error:
            raise TrialError(f"refusing to overwrite {path}; choose a fresh pilot root") from error
        return path


def json_text(value):
    return json.dumps(value, indent=2, ensure_ascii=False) + "\n"


def dev_config(root):
    quote = lambda path: json.dumps(str(path))
    return ("[paths]\nscan_roots = []\nrepo_paths = [" +
            ", ".join(quote(root / route) for route in ("A-herdr-dev", "B-sidecar")) + "]\n" +
            "state_dir = " + quote(root / "dev/state") + "\n" +
            "worktree_root = " + quote(root / "worktrees/A") + "\n" +
            "tries_root = " + quote(root / "unused-tries") + "\n" +
            "project_root = " + quote(root / "unused-projects") + "\n" +
            '[runtime]\nbackend = "herdr"\n')


def route_instructions(root, route):
    return f"""# Route {route}: manual trial (not yet run)

Pilot root: `{root}`. Canonical clone: `{root / ('A-herdr-dev' if route == 'A' else 'B-sidecar')}`.
Read the harness README, then implement `TASKS.md` in two separate worktrees
from `trial-seed`. Use branches `{('trial/a-' if route == 'A' else 'b-')}json` and
`{('trial/a-' if route == 'A' else 'b-')}unicode`. Route A uses Herdr with task-free dev worktree
commands. Route B uses Sidecar's worktree workflow with the isolated config
and state paths recorded in `worktree-trial.json`.

- [ ] Record the tool versions, agent/model, and task start time in feedback.json.
- [ ] Run baseline `go test ./...`; the feature acceptance checks initially fail.
- [ ] Create and open a worktree without `dev start`, `dev adopt`, or a dev Task.
- [ ] Implement the assigned feature and run its acceptance command.
- [ ] Commit and push only the feature branch to the local origin.
- [ ] Keep the checkout while review is open or work is not integrated.
- [ ] Stop the actual agent/recorder before final history capture; do not infer
      writer exit from an idle label or file stability.
- [ ] Merge into this route's local main explicitly. Do not push main: the two
      routes intentionally share only the seed and separate feature refs.
- [ ] Leave the target checkout/runtime before reviewing cleanup.
- [ ] Review main containment and explicitly request local branch deletion
      after merging, so completed branches leave the local branch list.
      Keep the separate branch-preservation sanity check in the automated fixtures.
- [ ] Record commands, latency, blockers, retry count, and whether the flow felt
      intrusive. Leave unknown answers null instead of inventing a score.

No agent, TUI, cleanup, PR, td initialization, or human evaluation was run by
the generator. The local origin cannot supply a real forge PR state.
"""


def create(raw, dry_run=False):
    root = validate_destination(raw)
    plan = {"schema_version": 1, "action": "create", "dry_run": dry_run, "root": str(root),
            "routes": {"A": str(root / "A-herdr-dev"), "B": str(root / "B-sidecar")},
            "origin": str(root / "origin.git"),
            "effects": ["create one new external root", "seed one local bare origin",
                        "clone two independent checkouts at the same seed", "write isolated configs and blank feedback"],
            "manual_evaluation": "not_run"}
    if dry_run:
        return plan
    # No overwrite or reuse: mkdir is exclusive even if another process wins
    # after validation. Recheck parents before publishing the root.
    validate_destination(raw)
    try:
        root.mkdir(mode=0o700)
    except FileExistsError as error:
        raise TrialError("destination appeared after preview; choose a different new root") from error
    tree = Tree(root)
    origin = tree.path("origin.git")
    git(root, "init", "--bare", "--initial-branch=main", str(origin))
    seed = tree.mkdir("seed")
    for source in sorted((HERE / "seed").iterdir()):
        name = source.name.removesuffix(".txt")
        tree.write(Path("seed") / name, source.read_text(encoding="utf-8"))
    git(seed, "init", "--initial-branch=main")
    git(seed, "add", "--", "go.mod", "main.go", "main_test.go", "README.md", "TASKS.md")
    git(seed, "commit", "-m", "Seed textstat workflow trial")
    git(seed, "tag", "trial-seed")
    git(seed, "remote", "add", "origin", str(origin))
    git(seed, "push", "origin", "main", "refs/tags/trial-seed")
    seed_commit = git(seed, "rev-parse", "HEAD").stdout.strip()
    for directory in ("A-herdr-dev", "B-sidecar"):
        git(root, "clone", "--no-hardlinks", str(origin), str(tree.path(directory)))
    for directory in ("sidecar/state", "sidecar/data", "sidecar/cache", "sidecar/tmux", "dev/state", "worktrees/A"):
        tree.mkdir(directory)
    tree.write("sidecar/config.json", "{}\n")
    tree.write("dev/config.toml", dev_config(root))
    tree.write("acceptance.py", (HERE / "acceptance.py").read_text(encoding="utf-8"))
    tree.write("README.md", (HERE / "README.md").read_text(encoding="utf-8"))
    for route in ("A", "B"):
        tree.write(f"route-{route}.md", route_instructions(root, route))
    feedback = {"schema_version": 1, "status": "not_run", "routes": {}}
    for route in ("A", "B"):
        feedback["routes"][route] = {"tool_versions": None, "agent_model": None, "tasks": {
            task: {"started_at": None, "finished_at": None, "commands": [], "acceptance_passed": None,
                   "cleanup_attempts": None, "blockers": [], "felt_intrusive": None, "notes": None}
            for task in ("json", "unicode")}}
    tree.write("feedback.json", json_text(feedback))
    manifest = {**plan, "id": str(uuid.uuid4()), "root_identity": tree.identity,
                "seed_commit": seed_commit, "dev_config": str(root / "dev/config.toml"),
                "sidecar": {"config": str(root / "sidecar/config.json"), "state": str(root / "sidecar/state"),
                            "tmux_tmpdir": str(root / "sidecar/tmux")},
                "feedback": str(root / "feedback.json")}
    tree.write(MANIFEST, json_text(manifest))
    return manifest


def verify_manifest(tree):
    tree.check()
    path = tree.path(MANIFEST)
    data = json.loads(path.read_text(encoding="utf-8"))
    if (data.get("schema_version") != 1 or data.get("root") != str(tree.root)
            or data.get("root_identity") != tree.identity or not data.get("id")):
        raise TrialError("fixture manifest does not match this generated root; refusing cleanup checks")
    for route in data["routes"].values():
        path = Path(route)
        try:
            tree.path(path.relative_to(tree.root))
        except ValueError as error:
            raise TrialError("fixture route escapes its manifest root") from error
        common = Path(git(path, "rev-parse", "--path-format=absolute", "--git-common-dir").stdout.strip())
        if not common.is_relative_to(tree.root):
            raise TrialError("fixture Git common directory escaped its generated root")
    return data


def check_fixtures(dev, dry_run=False):
    dev = Path(dev)
    if not dev.is_absolute() or not dev.is_file() or not os.access(dev, os.X_OK):
        raise TrialError("--dev must name an absolute executable path; build ./cmd/dev into a temporary output first", 2)
    scenarios = ["merged_clean", "merged_branch_deleted", "unmerged_pushed", "dirty_code",
                 "ignored_specstory_tail", "branch_advanced_after_preview"]
    if dry_run:
        return {"schema_version": 1, "action": "check", "dry_run": True, "dev": str(dev),
                "scenarios": scenarios, "effects": ["create and remove only a new temporary fixture root",
                "run public dev cleanup with isolated user state and no runtime"]}
    # No caller-supplied cleanup root exists. Only this freshly allocated
    # temporary directory can be removed by the harness.
    parent = Path(tempfile.gettempdir()).resolve()
    with tempfile.TemporaryDirectory(prefix="dev-worktree-trial-check-", dir=parent) as temporary:
        root = Path(temporary) / "pilot"
        create(str(root))
        tree = Tree(root)
        manifest = verify_manifest(tree)
        home = tree.mkdir("check-home")
        env = command_env(home)
        repo = root / "A-herdr-dev"
        config = root / "dev/config.toml"

        def call(*args, cwd=repo):
            verify_manifest(tree)
            return run([dev, "--config", config, "--no-runtime", *args], cwd, env)

        archive = tree.mkdir("fixtures/history-archive")
        git(archive, "init", "--initial-branch=main", env=env)
        tree.write("fixtures/history-archive/README.md", "Synthetic trial archive; no user history.\n")
        git(archive, "add", "README.md", env=env)
        git(archive, "commit", "-m", "Seed synthetic archive", env=env)
        setup = json.loads(call("agent", "artifact", "setup", "--mode", "archive", "--source", "specstory",
                                "--capture", "project", "--archive", str(archive), "--protection", "off", "--json").stdout)
        call("agent", "artifact", "setup", "--apply", "--plan", setup["id"], "--yes", "--json")
        git(repo, "add", "--", ".gitignore", ".gitattributes", ".dev-cli/artifacts.toml", env=env)
        git(repo, "commit", "-m", "Synthetic ignored-history policy", env=env)
        worktrees = {}

        def add(name):
            path = tree.path("fixtures/worktrees/" + name)
            path.parent.mkdir(parents=True, exist_ok=True)
            git(repo, "worktree", "add", "-b", "fixture/" + name, str(path), "main", env=env)
            worktrees[name] = path
            return path

        def commit_change(path, content):
            tree.write(path.relative_to(root) / "fixture-change.txt", content)
            git(path, "add", "fixture-change.txt", env=env)
            git(path, "commit", "-m", "Synthetic fixture change", env=env)

        merged = add("merged-clean")
        commit_change(merged, "merged\n")
        git(repo, "merge", "--ff-only", "fixture/merged-clean", env=env)
        unmerged = add("unmerged-pushed")
        tree.write(unmerged.relative_to(root) / "pending-review.txt", "Pushed is not integrated.\n")
        git(unmerged, "add", "pending-review.txt", env=env)
        git(unmerged, "commit", "-m", "Synthetic pending review", env=env)
        git(unmerged, "push", "origin", "HEAD:refs/heads/fixture/unmerged-pushed", env=env)
        dirty = add("dirty-code")
        with (dirty / "main.go").open("a", encoding="utf-8") as handle:
            handle.write("\n// Synthetic uncommitted product edit.\n")
        dirty_bytes = (dirty / "main.go").read_bytes()
        history = add("history-tail")
        history_bytes = ("<!-- Generated by SpecStory, Markdown v2.1.0 -->\n\n# Synthetic fixture\n\n"
                         "<!-- Codex CLI Session 00000000-0000-4000-8000-000000000001 (fixture) -->\n\n"
                         "Synthetic initial capture.\nSynthetic final tail after product commit.\n")
        tree.write(history.relative_to(root) / HISTORY, history_bytes)
        if git(history, "status", "--porcelain", env=env).stdout:
            raise TrialError("ignored-history fixture is unexpectedly Git-dirty; no cleanup assertions were run")
        advanced = add("advanced-after-preview")
        preview_oid = git(advanced, "rev-parse", "HEAD", env=env).stdout.strip()
        flags = ("work", "sweep", "--merged-worktrees", "--base", "main", "--assume-no-runtime")
        preview = call(*flags)
        if str(advanced) not in preview.stdout:
            raise TrialError("preview did not offer the unchanged contained fixture; inspect current dev behavior")
        tree.write(advanced.relative_to(root) / "advanced.txt", "Branch advanced after the earlier preview.\n")
        git(advanced, "add", "advanced.txt", env=env)
        git(advanced, "commit", "-m", "Advance after preview", env=env)
        changed_oid = git(advanced, "rev-parse", "HEAD", env=env).stdout.strip()
        applied = call(*flags, "--apply", "--yes")
        registered = git(repo, "worktree", "list", "--porcelain", env=env).stdout
        branch_kept = git(repo, "show-ref", "--verify", "refs/heads/fixture/merged-clean", env=env, check=False).returncode == 0
        results = [
            {"case": "merged_clean", "passed": not merged.exists() and str(merged) not in registered and branch_kept},
            {"case": "unmerged_pushed", "passed": unmerged.is_dir() and str(unmerged) in registered},
            {"case": "dirty_code", "passed": dirty.is_dir() and (dirty / "main.go").read_bytes() == dirty_bytes},
            {"case": "ignored_specstory_tail", "passed": history.is_dir()
             and (history / HISTORY).read_text() == history_bytes
             and not git(history, "status", "--porcelain", env=env).stdout,
             "selected_file": HISTORY, "sha256": hashlib.sha256(history_bytes.encode()).hexdigest()},
            {"case": "branch_advanced_after_preview", "passed": preview_oid != changed_oid and advanced.is_dir()
             and str(advanced) in registered and git(advanced, "rev-parse", "HEAD", env=env).stdout.strip() == changed_oid},
        ]
        # A second round explicitly requests deletion, preserving the first
        # round's independent proof of the ordinary branch-retaining behavior.
        delete_target = add("merged-delete")
        tree.write(delete_target.relative_to(root) / "completed.txt", "Explicitly integrated and ready for branch deletion.\n")
        git(delete_target, "add", "completed.txt", env=env)
        git(delete_target, "commit", "-m", "Synthetic completed branch", env=env)
        git(repo, "merge", "--ff-only", "fixture/merged-delete", env=env)
        deletion_preview = call(*flags, "--delete-branches")
        if str(delete_target) not in deletion_preview.stdout:
            raise TrialError("branch-deletion preview did not offer the integrated fixture; inspect current dev behavior")
        deletion_apply = call(*flags, "--delete-branches", "--apply", "--yes")
        registered_after_delete = git(repo, "worktree", "list", "--porcelain", env=env).stdout
        branch_deleted = git(repo, "show-ref", "--verify", "refs/heads/fixture/merged-delete", env=env, check=False).returncode != 0
        original_branch_retained = git(repo, "show-ref", "--verify", "refs/heads/fixture/merged-clean", env=env, check=False).returncode == 0
        results.insert(1, {"case": "merged_branch_deleted", "passed": not delete_target.exists()
                           and str(delete_target) not in registered_after_delete and branch_deleted
                           and original_branch_retained})
        retained_after_deletion = {
            "unmerged_pushed": unmerged.is_dir() and str(unmerged) in registered_after_delete,
            "dirty_code": dirty.is_dir() and (dirty / "main.go").read_bytes() == dirty_bytes,
            "ignored_specstory_tail": history.is_dir() and (history / HISTORY).read_text() == history_bytes,
            "branch_advanced_after_preview": advanced.is_dir() and str(advanced) in registered_after_delete
                and git(advanced, "rev-parse", "HEAD", env=env).stdout.strip() == changed_oid,
        }
        for result in results:
            result["passed"] = result["passed"] and retained_after_deletion.get(result["case"], True)
        no_tasks = not list((root / "dev/state/tasks").glob("*.toml"))
        passed = all(item["passed"] for item in results) and no_tasks
        report = {"schema_version": 1, "action": "check", "passed": passed, "cases": results,
                  "dev": str(dev), "dev_version": call("--version").stdout.strip(),
                  "no_dev_tasks": no_tasks, "fixture_id": manifest["id"],
                  "preview_exit": preview.returncode, "apply_exit": applied.returncode,
                  "branch_deletion_preview_exit": deletion_preview.returncode,
                  "branch_deletion_apply_exit": deletion_apply.returncode,
                  "runtime": "none; acknowledgement limited to fixtures with no launched processes",
                  "limits": ["two public CLI invocations replan; this is not the same-plan stale/CAS test",
                             "no actual PR, agent, TUI, Herdr/Sidecar workflow, or human preference was tested"]}
        verify_manifest(tree)
    report["temporary_fixture_removed"] = True
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__, epilog=(
        "Requires Python 3.11+ and local Git. Examples: create --root /physical/external/pilot --dry-run; "
        "check --dev /absolute/built/dev. Exit: 0 success; 2 usage/missing input; 3 subprocess failure; 4 validation failure. "
        "Create refuses existing roots and never removes a persistent pilot."))
    commands = parser.add_subparsers(dest="command", required=True)
    make = commands.add_parser("create", help="make two identical clones in a NEW external directory")
    make.add_argument("--root", required=True, help="absolute absent path beneath an existing physical, non-Git parent")
    make.add_argument("--dry-run", action="store_true", help="validate and print effects without writing anything")
    check = commands.add_parser("check", help="exercise public dev cleanup only in a NEW temporary fixture")
    check.add_argument("--dev", required=True, help="absolute path to the current built dev executable")
    check.add_argument("--dry-run", action="store_true", help="print fixture scenarios without executing dev or writing files")
    args = parser.parse_args()
    try:
        result = create(args.root, args.dry_run) if args.command == "create" else check_fixtures(args.dev, args.dry_run)
        print(json_text(result), end="")
        return 0 if result.get("passed", True) else 4
    except (TrialError, OSError, ValueError) as error:
        print(f"worktree-trial: {error}", file=sys.stderr)
        return error.code if isinstance(error, TrialError) else 4


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env bash
set -euo pipefail

# Initialise project-knowledge-harness in a target repo:
# - Create TODO.md, backlog/README.md, pitfalls/README.md from templates
# - Append agent guidance to AGENTS.md / CLAUDE.md (auto-detect)
# - Append "Roadmap & lessons learned" snippet to README.md
# - Run todo-kanban.sh --validate-only at the end
#
# Idempotent: existing files are NEVER overwritten unless --force is given;
# snippets are appended only if a sentinel marker is not already present.
#
# Compatibility: macOS system Bash 3.2.

usage() {
  cat <<'EOF'
Usage: init.sh [OPTIONS]

Set up project-knowledge-harness files in a target project. Safe to re-run.

Options:
  --target DIR              Target project root (default: current directory)
  --project-name NAME       Substituted into <PROJECT NAME> placeholders
                            (default: basename of --target)
  --deployment MECH         One of: chezmoi | npm | pip | docker | none
                            Substituted into <DEPLOYMENT MECHANISM> /
                            <IGNORE FILE> placeholders. Default: none.
  --agent-contract FILE     Path (relative to --target) to the agent
                            contract file to receive the guidance snippet.
                            Default: auto-detect AGENTS.md, CLAUDE.md,
                            .opencode/AGENTS.md, .cursorrules. If none
                            exist, AGENTS.md is created.
  --readme FILE             Path to README.md (default: README.md in target).
                            Pass "" to skip the README snippet.
  --force                   Overwrite TODO.md / backlog/README.md /
                            pitfalls/README.md if they already exist.
  --no-validate             Skip the final todo-kanban.sh validation pass.
  -h, --help                Show this help and exit.

The script never edits .gitignore / .chezmoiignore — it only prints the
ignore rules you should add for the chosen deployment mechanism.
EOF
}

target="."
project_name=""
deployment="none"
agent_contract=""
readme_file=""
readme_explicit=0
force=0
do_validate=1

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --target) target="$2"; shift 2 ;;
    --project-name) project_name="$2"; shift 2 ;;
    --deployment) deployment="$2"; shift 2 ;;
    --agent-contract) agent_contract="$2"; shift 2 ;;
    --readme) readme_file="$2"; readme_explicit=1; shift 2 ;;
    --force) force=1; shift ;;
    --no-validate) do_validate=0; shift ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$deployment" in
  chezmoi|npm|pip|docker|none) ;;
  *) echo "Error: --deployment must be chezmoi|npm|pip|docker|none" >&2; exit 2 ;;
esac

target_abs="$(cd "$target" && pwd)"
[ -n "$project_name" ] || project_name="$(basename "$target_abs")"

skill_dir="$(cd "$(dirname "$0")/.." && pwd)"
assets_dir="$skill_dir/assets"
scripts_dir="$skill_dir/scripts"

if [ ! -d "$assets_dir" ]; then
  echo "Error: assets/ not found next to scripts/ (looked in $assets_dir)" >&2
  exit 1
fi

# Map deployment to (DEPLOYMENT MECHANISM, IGNORE FILE) labels and ignore-rule lines.
deployment_label=""
ignore_label=""
ignore_lines=""
case "$deployment" in
  chezmoi)
    deployment_label="chezmoi"
    ignore_label=".chezmoiignore.tmpl"
    ignore_lines=$'TODO.md\nbacklog/**\npitfalls/**'
    ;;
  npm)
    deployment_label="npm"
    ignore_label=".npmignore (or package.json \"files\")"
    ignore_lines=$'TODO.md\nbacklog/\npitfalls/'
    ;;
  pip)
    deployment_label="pip / setuptools"
    ignore_label="MANIFEST.in"
    ignore_lines=$'exclude TODO.md\nrecursive-exclude backlog *\nrecursive-exclude pitfalls *'
    ;;
  docker)
    deployment_label="Docker"
    ignore_label=".dockerignore"
    ignore_lines=$'TODO.md\nbacklog/\npitfalls/'
    ;;
  none)
    deployment_label="N/A (no packaging — these files stay in the repo)"
    ignore_label="N/A"
    ignore_lines=""
    ;;
esac

# Trailing exclusion clause appended to template sentences ending in
# `<DEPLOY EXCLUDE NOTE>`. Empty for `none` so the sentence just stops.
if [ "$deployment" = "none" ]; then
  deploy_exclude_clause=""
else
  deploy_exclude_clause=" Excluded from ${deployment_label} (see ${ignore_label})."
fi

# Sentinel markers so re-runs don't double-append snippets.
agent_marker="<!-- project-knowledge-harness:agent-guidance -->"
readme_marker="<!-- project-knowledge-harness:readme-roadmap -->"

# --- helper: render a template by replacing placeholders, write to stdout
render_template() {
  local src="$1"
  sed \
    -e "s|<PROJECT NAME>|${project_name}|g" \
    -e "s|<LINK TO PROJECT AGENT CONTRACT, e.g. AGENTS.md or CLAUDE.md>|AGENTS.md|g" \
    -e "s|<DEPLOY EXCLUDE NOTE>|${deploy_exclude_clause}|g" \
    "$src"
}

create_file() {
  local rel="$1"
  local src="$2"
  local dest="$target_abs/$rel"
  mkdir -p "$(dirname "$dest")"
  if [ -e "$dest" ] && [ "$force" -eq 0 ]; then
    echo "skip: $rel already exists (use --force to overwrite)"
    return
  fi
  render_template "$src" > "$dest"
  echo "create: $rel"
}

append_snippet() {
  local rel="$1"
  local src="$2"
  local marker="$3"
  local dest="$target_abs/$rel"

  if [ -e "$dest" ] && grep -qF "$marker" "$dest" 2>/dev/null; then
    echo "skip: $rel already contains $marker"
    return
  fi

  mkdir -p "$(dirname "$dest")"
  if [ ! -e "$dest" ]; then
    : > "$dest"
  fi
  # Keep the closing marker inside the HTML comment so "(end)" doesn't
  # render as visible text in Markdown.
  local end_marker="${marker% -->} (end) -->"
  {
    printf '\n%s\n' "$marker"
    render_template "$src"
    printf '%s\n' "$end_marker"
  } >> "$dest"
  echo "append: $rel"
}

detect_agent_contract() {
  local candidates=(AGENTS.md CLAUDE.md .opencode/AGENTS.md .cursorrules)
  local c
  for c in "${candidates[@]}"; do
    if [ -e "$target_abs/$c" ]; then
      echo "$c"
      return
    fi
  done
  echo "AGENTS.md"
}

if [ -z "$agent_contract" ]; then
  agent_contract="$(detect_agent_contract)"
fi

if [ "$readme_explicit" -eq 0 ]; then
  readme_file="README.md"
fi

echo "Project root: $target_abs"
echo "Project name: $project_name"
echo "Deployment:   $deployment ($deployment_label, ignore in $ignore_label)"
echo "Agent contract: $agent_contract"
echo "README:       ${readme_file:-<skipped>}"
echo

create_file "TODO.md" "$assets_dir/TODO.md.template"
create_file "backlog/README.md" "$assets_dir/backlog-README.md.template"
create_file "pitfalls/README.md" "$assets_dir/pitfalls-README.md.template"

# Seed backlog/inbox.md if missing. Keep it tiny — sweep-inbox.sh handles it.
if [ ! -e "$target_abs/backlog/inbox.md" ]; then
  mkdir -p "$target_abs/backlog"
  cat > "$target_abs/backlog/inbox.md" <<'INBOX_EOF'
# Inbox

Quick-capture area. Drop loose lines here when priority/effort/wording
isn't clear yet; run the `project-knowledge-harness` skill's `sweep-inbox.sh`
later to formalize them into `TODO.md`.

Lines starting with `#` and blank lines are ignored. Free-form lines
prompt the sweeper for missing fields; lines shaped like
`- priority=P3 effort=M title="X" description="Y"` (or short aliases
`p=`/`e=`/`t=`/`d=`) are processed automatically by `--batch`.

<!-- inbox entries below this line; mix free-form and key=value freely -->

INBOX_EOF
  echo "create: backlog/inbox.md"
else
  echo "skip: backlog/inbox.md already exists"
fi

append_snippet "$agent_contract" "$assets_dir/agent-guidance.md.template" "$agent_marker"
if [ -n "$readme_file" ]; then
  append_snippet "$readme_file" "$assets_dir/readme-roadmap.md.template" "$readme_marker"
fi

echo
if [ -n "$ignore_lines" ]; then
  echo "Add these lines to $ignore_label so backlog/ and pitfalls/ aren't shipped:"
  printf '%s\n' "$ignore_lines" | sed 's/^/  /'
  echo
fi

if [ "$do_validate" -eq 1 ] && [ -x "$scripts_dir/todo-kanban.sh" ]; then
  echo "Validating TODO.md..."
  ( cd "$target_abs" && "$scripts_dir/todo-kanban.sh" --validate-only TODO.md )
fi

cat <<EOF

Next steps (the project-knowledge-harness skill's scripts are NOT copied here —
invoke them from the skill folder; they act on the current directory's TODO.md):
  1. Open TODO.md and replace the example items with real ones, or insert
     structured entries with:
       $scripts_dir/add-todo.sh --priority P3 --effort M \\
         --title "Title" --description "Description"
  2. Drop loose ideas into backlog/inbox.md; formalize later via:
       $scripts_dir/sweep-inbox.sh             # interactive
       $scripts_dir/sweep-inbox.sh --batch     # only key=value lines
  3. Add ignore rules above to your packaging/deployment ignore file.
  4. Run the kanban renderer when you want a quick board view:
       $scripts_dir/todo-kanban.sh
  5. Promote shipped items via:
       $scripts_dir/promote-todo.sh --title "<substring>" --summary "<what shipped>"

Run these from $target_abs (or pass --file / a TODO_FILE path) so they find
this project's TODO.md rather than one in the current shell directory.
EOF

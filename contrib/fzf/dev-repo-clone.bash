# Source this file from Bash or Zsh after installing dev, jq, and fzf.
# Populate the private candidate cache once with: dev repo remote --refresh

dev-repo-clone-fzf() {
  local selected rc ref
  selected="$({
    dev repo remote --cached --json |
      jq -r '.[] | select(.repo.clone_url != null and .repo.clone_url != "") | select((.repo.clone_url | test("[\\t\\r\\n]")) | not) | [.repo.clone_url, (.repo.forge + ":" + .repo.full_name), (.repo.visibility // ""), (.local_path // ""), ((.repo.description // "") | gsub("[\\t\\r\\n]+"; " "))] | @tsv' |
      fzf --delimiter=$'\t' --with-nth=2.. --prompt='Repository to clone> '
  })"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    return "$rc"
  fi
  [ -n "$selected" ] || return 0

  ref=${selected%%$'\t'*}
  [ -n "$ref" ] || return 1
  dev repo clone "$ref"
}

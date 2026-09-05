#!/usr/bin/env bash
# readme-check.sh — run every README example that is marked as checkable.
#
# A fenced `sh` block whose lines include `# check` is extracted and executed
# with `bash -euo pipefail` against a freshly built sonar, in a throwaway HOME
# with its own daemon, socket and database. Nothing touches the machine's real
# config, and no example is allowed to kill anything that is really running.
#
#   scripts/readme-check.sh                 # build sonar, check README.md
#   SONAR_BIN=/path/to/sonar scripts/readme-check.sh docs/OTHER.md
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
readme=${1:-$root/README.md}

work=$(mktemp -d "${TMPDIR:-/tmp}/sonar-readme.XXXXXX")
bin=$work/bin
home=$work/home
project=$work/project
mkdir -p "$bin" "$home" "$project"

cleanup() {
  "$bin/sonar" daemon stop >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

if [ -n "${SONAR_BIN:-}" ]; then
  cp "$SONAR_BIN" "$bin/sonar"
else
  (cd "$root" && go build -o "$bin/sonar" .)
fi

export HOME=$home
export USERPROFILE=$home
export PATH=$bin:$PATH
export NO_COLOR=1
export SONAR_SOCKET=$work/daemon.sock
export SONAR_DB=$work/sonar.db
unset XDG_RUNTIME_DIR
unset SONAR_NO_HINTS

# One daemon for the whole run: the examples that need it (start, rename,
# history) must not each pay for an autostart.
"$bin/sonar" serve --detach >/dev/null
for _ in $(seq 1 50); do
  [ -S "$SONAR_SOCKET" ] && break
  sleep 0.1
done

# A git repo, so group inference has a root to find and `sonar init` has
# somewhere to write.
git init -q "$project"
# Physical path: on macOS the temp directory is reached through a symlink, and
# a project whose cwd is the symlinked spelling would not match its own config.
cd "$(cd "$project" && pwd -P)"

# Extract the checkable blocks. State machine over the fences: a ```sh block is
# collected, and kept only if one of its lines is the `# check` marker.
blocks=$work/blocks
mkdir -p "$blocks"
awk -v out="$blocks" '
  /^```sh$/ && !inblock { inblock = 1; n = 0; checked = 0; next }
  /^```/ && inblock {
    if (checked) {
      count++
      file = sprintf("%s/%03d.sh", out, count)
      for (i = 1; i <= n; i++) print body[i] > file
      close(file)
    }
    inblock = 0
    next
  }
  inblock {
    body[++n] = $0
    if ($0 ~ /^[[:space:]]*#[[:space:]]*check[[:space:]]*$/) checked = 1
  }
' "$readme"

shopt -s nullglob
files=("$blocks"/*.sh)
if [ ${#files[@]} -eq 0 ]; then
  echo "readme-check: no blocks marked '# check' in $readme" >&2
  exit 1
fi

failed=0
for file in "${files[@]}"; do
  name=$(basename "$file")
  if output=$(bash -euo pipefail "$file" 2>&1); then
    printf 'ok   %s\n' "$name"
  else
    failed=$((failed + 1))
    printf 'FAIL %s\n' "$name"
    sed 's/^/     | /' "$file"
    printf '     --- output ---\n'
    printf '%s\n' "$output" | sed 's/^/     | /'
  fi
done

echo
if [ "$failed" -ne 0 ]; then
  echo "readme-check: $failed of ${#files[@]} blocks failed"
  exit 1
fi
echo "readme-check: ${#files[@]} blocks ok"

#!/usr/bin/env bash
# Build Anvil and print the launch command for driving the TUI in a
# throwaway sandbox, so manual testing never touches the real config,
# session database, or project state.
#
# Usage:
#   scripts/tui-test.sh            # build + print launch details
#   scripts/tui-test.sh --reset    # also wipe the sandbox first
#   scripts/tui-test.sh --clean    # remove the binary and sandbox, then exit
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BINARY="${ANVIL_TUI_BINARY:-/tmp/anvil-tui-test}"
SANDBOX="${ANVIL_TUI_SANDBOX:-/tmp/anvil-tui-sandbox}"

case "${1:-}" in
--clean)
	rm -rf "$BINARY" "$SANDBOX"
	echo "Removed $BINARY and $SANDBOX"
	exit 0
	;;
--reset)
	rm -rf "$SANDBOX"
	;;
esac

mkdir -p "$SANDBOX/config" "$SANDBOX/data"
if [ ! -f "$SANDBOX/config/anvil.json" ]; then
	cat >"$SANDBOX/config/anvil.json" <<'JSON'
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json"
}
JSON
fi

echo "Building $BINARY..." >&2
(cd "$REPO_ROOT" && go build -o "$BINARY" .)

cat <<EOF
binary:  $BINARY
cwd:     $REPO_ROOT
args:    --cwd $REPO_ROOT --data-dir $SANDBOX/data
env:     ANVIL_GLOBAL_CONFIG=$SANDBOX/config TERM=xterm-256color
EOF

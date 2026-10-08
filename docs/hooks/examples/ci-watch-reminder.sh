#!/usr/bin/env bash

# CI watch reminder hook for Anvil
#
# After any bash call that pushes a branch or opens a pull request, this
# hook appends a reminder to the tool result telling the agent to start a
# background CI watcher (see <ci_checks> in the bash tool description).
#
# It only adds context: it never approves, blocks or rewrites the call, so
# the normal permission flow still applies.
#
# Config:
#   {"matcher": "^bash$", "command": "/path/to/ci-watch-reminder.sh",
#    "timeout": 5}

set -euo pipefail

CMD="${ANVIL_TOOL_INPUT_COMMAND:-}"

# Matches "git push", "git -C <dir> push" and "gh pr create" anywhere in
# a chained command.
GIT_DIR_OPT="([[:space:]]+-C[[:space:]]+(\"[^\"]*\"|'[^']*'|[^[:space:]]+))?"
PATTERN="(^|[^[:alnum:]_-])(git${GIT_DIR_OPT}[[:space:]]+push|gh[[:space:]]+pr[[:space:]]+create)([^[:alnum:]_-]|\$)"

if ! printf '%s' "$CMD" | grep -qE "$PATTERN"; then
    exit 0
fi

if printf '%s' "$CMD" | grep -qE -- '--dry-run'; then
    exit 0
fi

cat <<'EOF'
{"context": "CI reminder: if this push or PR creation succeeded and the branch has a pull request, start one background CI watcher now as described in <ci_checks> of the bash tool (run_in_background=true, gh pr checks <pr> --watch --fail-fast). Keep working while it runs; when nothing else is left, follow <ci_checks> on whether to wait for it or end your turn. Skip this if a watcher for this PR is already running (check job_list) or the user said not to wait for CI."}
EOF

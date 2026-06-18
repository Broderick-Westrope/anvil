#!/usr/bin/env bash
# capture.sh — Intercept Claude Code's Anthropic API requests via mitmproxy.
#
# Prerequisites:
#   brew install mitmproxy   (or pip install mitmproxy)
#   Claude Code installed and authenticated (run `claude` at least once)
#
# Usage:
#   ./capture.sh                      # Uses default model (haiku) and prompt ("hi")
#   ./capture.sh --model sonnet       # Capture with a specific model
#   ./capture.sh --prompt "explain x" # Capture with a specific prompt
#   ./capture.sh --allow-api-key        # Allow ANTHROPIC_API_KEY (default: unset for OAuth)
#   ./capture.sh --port 8888          # Use a different proxy port
#
# Output:
#   capture.json in the current directory (or --outfile <path>)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Defaults.
MODEL="haiku"
PROMPT="hi"
PORT=8080
OUTFILE="${SCRIPT_DIR}/capture.json"
ALLOW_API_KEY=false

# Parse arguments.
while [[ $# -gt 0 ]]; do
    case "$1" in
        --model)       MODEL="$2";   shift 2 ;;
        --prompt)      PROMPT="$2";  shift 2 ;;
        --port)        PORT="$2";    shift 2 ;;
        --outfile)     OUTFILE="$2"; shift 2 ;;
        --allow-api-key) ALLOW_API_KEY=true; shift ;;
        -h|--help)
            sed -n '2,/^$/s/^# //p' "$0"
            exit 0
            ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# Check dependencies.
if ! command -v mitmdump &>/dev/null; then
    echo "ERROR: mitmdump not found. Install mitmproxy:" >&2
    echo "  brew install mitmproxy" >&2
    echo "  # or: pip install mitmproxy" >&2
    exit 1
fi

if ! command -v claude &>/dev/null; then
    echo "ERROR: claude CLI not found on PATH." >&2
    exit 1
fi

echo "==> Starting mitmdump on port ${PORT}..."
mitmdump \
    --listen-port "${PORT}" \
    --set "outfile=${OUTFILE}" \
    -s "${SCRIPT_DIR}/capture.py" \
    --quiet &
MITM_PID=$!

# Give mitmdump time to bind.
sleep 1

if ! kill -0 "${MITM_PID}" 2>/dev/null; then
    echo "ERROR: mitmdump failed to start. Is port ${PORT} in use?" >&2
    exit 1
fi

cleanup() {
    echo "==> Stopping mitmdump (PID ${MITM_PID})..."
    kill "${MITM_PID}" 2>/dev/null || true
    wait "${MITM_PID}" 2>/dev/null || true
}
trap cleanup EXIT

echo "==> Running: claude -p . --model ${MODEL} \"${PROMPT}\""
echo "    (proxied through localhost:${PORT})"

# Build the env for the Claude CLI invocation.
PROXY_ENV=(
    HTTPS_PROXY="http://127.0.0.1:${PORT}"
    HTTP_PROXY="http://127.0.0.1:${PORT}"
    NODE_TLS_REJECT_UNAUTHORIZED=0
)

if [[ "${ALLOW_API_KEY}" == "false" ]]; then
    echo "    Unsetting ANTHROPIC_API_KEY to use OAuth credentials (pass --allow-api-key to override)"
    PROXY_ENV+=(ANTHROPIC_API_KEY= ANTHROPIC_AUTH_TOKEN=)
fi

# Route Claude Code through the proxy. NODE_TLS_REJECT_UNAUTHORIZED=0
# is required because mitmproxy uses its own CA certificate.
env "${PROXY_ENV[@]}" claude -p . --model "${MODEL}" "${PROMPT}" 2>/dev/null || true

# Give mitmdump a moment to flush.
sleep 1

echo ""
echo "==> Capture complete: ${OUTFILE}"

if [[ -f "${OUTFILE}" ]]; then
    COUNT=$(python3 -c "import json; print(len(json.load(open('${OUTFILE}'))))" 2>/dev/null || echo "?")
    echo "    ${COUNT} request(s) captured."
    echo ""
    echo "    To compare against reference:"
    echo "      ${SCRIPT_DIR}/compare.sh ${OUTFILE}"
fi

# OAuth Debug Tools

Reverse-engineer Anthropic's OAuth API protocol by intercepting real Claude
Code requests and comparing them against a known-good reference.

## Prerequisites

```bash
brew install mitmproxy   # or: pip install mitmproxy
```

Claude Code must be installed and authenticated (`claude` on PATH).

## Quick Start

```bash
cd scripts/oauth-debug

# 1. Capture a real Claude Code request.
./capture.sh

# 2. Compare against the reference protocol.
./compare.sh
```

## Files

| File             | Purpose                                                     |
|------------------|-------------------------------------------------------------|
| `capture.py`     | mitmproxy addon that filters Anthropic API requests and     |
|                  | writes a structured JSON snapshot.                          |
| `capture.sh`     | Orchestration script: starts mitmproxy, runs Claude Code    |
|                  | through the proxy, stops mitmproxy. Produces `capture.json`.|
| `reference.json` | Known-good protocol snapshot: headers, query params, beta   |
|                  | flags, endpoints, system message format.                    |
| `compare.sh`     | Diffs a capture against the reference and reports missing,  |
|                  | extra, or changed values.                                   |

## Usage

### Capture with different models

```bash
./capture.sh --model sonnet
./capture.sh --model "claude-opus-4-6-20250514"
```

### Capture with a specific prompt

```bash
./capture.sh --prompt "What is 2+2?"
```

### Use a different port

```bash
./capture.sh --port 9090
```

### Compare a specific capture file

```bash
./compare.sh /path/to/other-capture.json
```

## When to Use

Run this when:

- Anvil or BroCode starts returning 401 errors with valid OAuth tokens.
- A new Claude Code version is released (headers/betas may change).
- Community projects report protocol changes.

## Workflow

1. **Capture** a real Claude Code request via mitmproxy.
2. **Compare** the capture against `reference.json`.
3. **Update** Anvil's headers/betas to match.
4. **Update** `reference.json` with the new known-good values.

## How It Works

`capture.sh` sets `HTTPS_PROXY` and `NODE_TLS_REJECT_UNAUTHORIZED=0`
so that Claude Code's requests flow through mitmproxy. The
`capture.py` addon filters for `api.anthropic.com`, extracts headers,
query params, and system messages, and writes them to JSON. Bearer
tokens are automatically redacted.

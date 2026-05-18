#!/usr/bin/env bash
# compare.sh — Diff a mitmproxy capture against the protocol reference.
#
# Usage:
#   ./compare.sh [capture.json]
#
# Reads the first request from the capture file and compares headers,
# query params, and beta flags against reference.json. Reports
# missing, extra, and changed values.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CAPTURE="${1:-${SCRIPT_DIR}/capture.json}"
REFERENCE="${SCRIPT_DIR}/reference.json"

if [[ ! -f "${CAPTURE}" ]]; then
    echo "ERROR: Capture file not found: ${CAPTURE}" >&2
    echo "Run ./capture.sh first, or pass a capture file as argument." >&2
    exit 1
fi

if [[ ! -f "${REFERENCE}" ]]; then
    echo "ERROR: Reference file not found: ${REFERENCE}" >&2
    exit 1
fi

python3 - "${CAPTURE}" "${REFERENCE}" <<'PYEOF'
import json
import sys

capture_path = sys.argv[1]
reference_path = sys.argv[2]

with open(capture_path) as f:
    captures = json.load(f)

with open(reference_path) as f:
    reference = json.load(f)

if not captures:
    print("ERROR: No requests in capture file.")
    sys.exit(1)

# Use the first /messages request, or fall back to the first request.
cap = None
for c in captures:
    if "/messages" in c.get("path", ""):
        cap = c
        break
if cap is None:
    cap = captures[0]

print(f"Comparing capture ({cap.get('timestamp', '?')}) against reference...")
print(f"  Captured: {cap['method']} {cap['url']}")
print(f"  Model:    {cap.get('body', {}).get('model', '?')}")
print()

issues = []
info = []

# --- Check endpoint ---
ref_endpoint = reference["endpoints"]["api"]
if not cap["url"].startswith(ref_endpoint.rsplit("/", 1)[0]):
    issues.append(f"ENDPOINT: Expected base {ref_endpoint}, got {cap['url']}")

# --- Check query params ---
ref_params = reference.get("query_params", {})
cap_params = cap.get("query_params", {})
for key, expected in ref_params.items():
    if key not in cap_params:
        issues.append(f"QUERY PARAM MISSING: ?{key}={expected}")
    elif cap_params[key] != expected:
        issues.append(f"QUERY PARAM CHANGED: ?{key} expected={expected} got={cap_params[key]}")
for key in cap_params:
    if key not in ref_params:
        info.append(f"QUERY PARAM EXTRA: ?{key}={cap_params[key]}")

# --- Check required headers ---
ref_headers = reference["headers"]["required"]
cap_headers = cap.get("headers", {})

# Case-insensitive header lookup.
cap_headers_lower = {k.lower(): (k, v) for k, v in cap_headers.items()}

for ref_name, ref_value in ref_headers.items():
    lower = ref_name.lower()
    if lower not in cap_headers_lower:
        issues.append(f"HEADER MISSING: {ref_name}")
    else:
        _, actual = cap_headers_lower[lower]
        # Skip value comparison for dynamic/template headers.
        if ref_value.startswith("<") or "redacted" in actual:
            continue
        # Prefix-match for templated values like "claude-cli/<version> ...".
        if "<" in ref_value:
            prefix = ref_value.split("<")[0]
            suffix = ref_value.split(">")[-1] if ">" in ref_value else ""
            if actual.startswith(prefix) and actual.endswith(suffix):
                continue
        if actual != ref_value:
            issues.append(f"HEADER CHANGED: {ref_name}\n    expected: {ref_value}\n    got:      {actual}")

# Check must-not-send headers.
must_not = reference["headers"].get("must_not_send", {})
for name in must_not:
    if name.lower() in cap_headers_lower:
        issues.append(f"HEADER MUST NOT SEND: {name} is present (value: {cap_headers_lower[name.lower()][1]})")

# --- Check beta flags ---
beta_key = "anthropic-beta"
if beta_key in cap_headers_lower:
    _, beta_val = cap_headers_lower[beta_key]
    cap_betas = set(b.strip() for b in beta_val.split(",") if b.strip())
else:
    cap_betas = set()

ref_beta_flags = reference.get("beta_flags", {})
ref_always = set(ref_beta_flags.get("always", []))
ref_non_haiku = set(ref_beta_flags.get("non_haiku", []))
ref_model_specific = set(ref_beta_flags.get("model_4_6_and_4_7", []))
ref_all_known = ref_always | ref_non_haiku | ref_model_specific

for b in ref_always:
    if b not in cap_betas:
        issues.append(f"BETA FLAG MISSING (always required): {b}")

unknown_betas = cap_betas - ref_all_known
for b in sorted(unknown_betas):
    info.append(f"BETA FLAG NEW/UNKNOWN: {b}")

# --- Check system messages ---
system_msgs = cap.get("body", {}).get("system_messages", [])
if system_msgs:
    has_billing = any("x-anthropic-billing-header" in s for s in system_msgs)
    has_identity = any("You are Claude Code" in s for s in system_msgs)
    if not has_billing:
        issues.append("SYSTEM MESSAGE MISSING: billing header (x-anthropic-billing-header)")
    if not has_identity:
        issues.append("SYSTEM MESSAGE MISSING: identity prefix ('You are Claude Code...')")
else:
    info.append("SYSTEM MESSAGES: None captured (body may not have been parsed).")

# --- Report ---
print("=" * 60)
if issues:
    print(f"  ISSUES FOUND: {len(issues)}")
    print("=" * 60)
    for i, issue in enumerate(issues, 1):
        print(f"  {i}. {issue}")
else:
    print("  NO ISSUES - capture matches reference protocol.")
    print("=" * 60)

if info:
    print()
    print(f"  INFO ({len(info)}):")
    for note in info:
        print(f"    - {note}")

print()

# All headers side-by-side for manual inspection.
print("--- All captured headers ---")
for name, value in sorted(cap_headers.items(), key=lambda x: x[0].lower()):
    # Truncate long values.
    display = value if len(value) <= 100 else value[:100] + "..."
    print(f"  {name}: {display}")

sys.exit(1 if issues else 0)
PYEOF

#!/usr/bin/env bash
# Reads changed paths, one per line, and prints "true" when any of them can
# affect the build, tests or lint, and "false" when every one is prose that
# nothing compiles, embeds or tests.
#
# The safe list is deliberately short. A path not on it counts as code, so a
# new kind of file runs the full checks until someone decides otherwise.
# Markdown inside internal/ is not safe: tool descriptions, prompt templates
# and builtin skills are embedded into the binary.
#
# No input at all also prints "true", so a broken diff can't skip checks.

set -euo pipefail

seen=false
while IFS= read -r path; do
    [ -n "$path" ] || continue
    seen=true
    case "$path" in
    plans/* | docs/*) ;;
    */*) echo true; exit 0 ;;
    *.md) ;;
    *) echo true; exit 0 ;;
    esac
done

if [ "$seen" = true ]; then
    echo false
else
    echo true
fi

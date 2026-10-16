#!/usr/bin/env bash
# Reads `git diff --raw --no-renames` output and prints "true" when any
# change can affect the build, tests or lint, and "false" when every change
# is a regular Markdown file that nothing compiles, embeds or tests.
#
# Only Markdown is safe, and only at the repo root or under plans/ or docs/.
# Anything else counts as code: Go files under docs/ still build and test,
# Markdown inside internal/ is embedded (tool descriptions, prompt templates,
# builtin skills), and a symlink or executable could point anywhere. A new
# kind of file runs the full checks until someone decides otherwise.
#
# No input at all also prints "true", so a broken diff can't skip checks.

set -euo pipefail

regular_or_absent() {
    [ "$1" = 100644 ] || [ "$1" = 000000 ]
}

seen=false
while IFS=$'\t' read -r meta path; do
    [ -n "$meta" ] || continue
    seen=true
    read -r old_mode new_mode _ <<<"${meta#:}"
    if ! regular_or_absent "$old_mode" || ! regular_or_absent "$new_mode"; then
        echo true
        exit 0
    fi
    case "$path" in
    plans/*.md | docs/*.md) ;;
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

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

# Reports whether one `git diff --raw` line is a prose-only change: an
# add, modify or delete of a regular Markdown file. Renames and copies carry
# two paths, so they never count, whatever the caller's diff flags.
is_prose() {
    local meta=$1 path=$2 old_mode new_mode status
    read -r old_mode new_mode _ _ status <<<"${meta#:}"
    case "$status" in
    A | M | D) ;;
    *) return 1 ;;
    esac
    regular_or_absent "$old_mode" && regular_or_absent "$new_mode" || return 1
    # In a case pattern `*` also matches `/`, so these cover nested files.
    case "$path" in
    plans/*.md | docs/*.md) return 0 ;;
    */*) return 1 ;;
    *.md) return 0 ;;
    *) return 1 ;;
    esac
}

seen=false
# The `|| [ -n "$meta" ]` keeps a last line that has no trailing newline.
while IFS=$'\t' read -r meta path || [ -n "$meta" ]; do
    [ -n "$meta" ] || continue
    seen=true
    if ! is_prose "$meta" "$path"; then
        echo true
        exit 0
    fi
done

if [ "$seen" = true ]; then
    echo false
else
    echo true
fi

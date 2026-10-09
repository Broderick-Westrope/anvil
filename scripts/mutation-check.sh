#!/usr/bin/env bash

set -euo pipefail

base="${1:-origin/main}"
gremlins_version="v0.6.0"
report="$(mktemp "${TMPDIR:-/tmp}/gremlins-report.XXXXXX")"
trap 'rm -f "$report"' EXIT

if ! git rev-parse --verify --quiet "${base}^{commit}" >/dev/null; then
    echo "Base ref '$base' not found. Fetch it first (CI needs fetch-depth: 0)." >&2
    exit 1
fi

added_lines=$(git diff --merge-base "$base" -U0 -- '*.go' ':(exclude)*_test.go' |
    grep -E '^\+' | grep -cv '^+++ ' || true)
if [ "$added_lines" -eq 0 ]; then
    echo "No changed Go lines outside tests since $base; nothing to mutate."
    exit 0
fi
echo "Mutating $added_lines changed Go lines since $base."

go clean -testcache

gremlins_status=0
go run "github.com/go-gremlins/gremlins/cmd/gremlins@${gremlins_version}" \
    unleash --diff "$base" --output "$report" . || gremlins_status=$?
if [ "$gremlins_status" -ne 0 ]; then
    echo "gremlins exited with status $gremlins_status." >&2
    exit "$gremlins_status"
fi
if [ ! -s "$report" ]; then
    echo "gremlins wrote no report; the run was probably interrupted." >&2
    exit 1
fi

survivors=$(jq -r '
    .files[]? as $file
    | $file.mutations[]?
    | select(.status == "LIVED" or .status == "NOT COVERED")
    | [$file.file_name, .line, .column, .status, .type] | @tsv
' "$report")

# Go coverage never instruments package-level const declarations, so their
# mutants are always NOT COVERED however well the values are tested. Set
# them aside, but list them so they stay visible.
if [ -n "$survivors" ]; then
    classified=$(printf '%s\n' "$survivors" | go run "$(dirname "$0")/mutation-const")
    consts=$(printf '%s\n' "$classified" | grep $'^const\t' | cut -f2- || true)
    survivors=$(printf '%s\n' "$classified" | grep $'^keep\t' | cut -f2- || true)
    if [ -n "$consts" ]; then
        echo
        echo "Ignoring mutants on package-level constants (Go coverage cannot reach them):"
        while IFS=$'\t' read -r file line column status mutation; do
            echo "  $file:$line:$column  $status  $mutation"
        done <<<"$consts"
    fi
fi

if [ -z "$survivors" ]; then
    echo "Every mutant on changed lines was killed."
    exit 0
fi

count=$(printf '%s\n' "$survivors" | wc -l | tr -d ' ')
echo
echo "$count mutant(s) on changed lines were not caught by tests:"

summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
{
    echo "## Mutation testing"
    echo
    echo "$count mutant(s) on changed lines were not caught by tests."
    echo
    echo "| Location | Status | Mutation |"
    echo "| --- | --- | --- |"
} >>"$summary"

while IFS=$'\t' read -r file line column status mutation; do
    if [ "$status" = "LIVED" ]; then
        message="A $mutation mutation here survived: no test failed when this line changed."
    else
        message="No test runs this line, so a $mutation mutation here goes unnoticed."
    fi
    echo "  $file:$line:$column  $status  $mutation"
    echo "| \`$file:$line:$column\` | $status | $mutation |" >>"$summary"
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
        echo "::error file=$file,line=$line,col=$column,title=Mutant $status::$message"
    fi
done <<<"$survivors"

echo
if [ "${MUTATION_EXEMPT:-}" = "true" ]; then
    echo "The PR is labelled mutation-exempt, so these are reported without failing."
    exit 0
fi
echo "Add or strengthen tests so each of these makes a test fail."
exit 1

#!/usr/bin/env bash

set -euo pipefail

# Usage: mutation-check.sh <base-ref>
# Run by the CI mutation workflow only; it is too slow to run locally.
base="$1"

gremlins_version="v0.6.0"
config=".gremlins.yaml"
run_dir="$(mktemp -d)"
trap 'rm -rf "$run_dir"' EXIT
report="$run_dir/report.json"

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

GOBIN="$run_dir/bin" go install "github.com/go-gremlins/gremlins/cmd/gremlins@${gremlins_version}"

# Mutate only the packages the PR changes. A whole-repo run spends minutes
# gathering coverage for packages with nothing to mutate, and gremlins times
# each mutant out at a multiple of that coverage time, so hanging mutants
# get slower too. A directory whose ancestor also changed is covered by the
# ancestor's run, which scans ./dir/....
dirs=$(git diff --merge-base "$base" --name-only --diff-filter=d -- '*.go' ':(exclude)*_test.go' |
    xargs -n1 dirname | sort -u |
    awk '{ for (i = 1; i <= n; i++) if (index($0 "/", kept[i] "/") == 1 || kept[i] == ".") next; kept[++n] = $0; print }')

repo_root=$(pwd)
reports=()
for dir in $dirs; do
    # Gremlins reads file paths relative to the directory it scans but
    # takes the changed-line diff from git, which reports paths from the
    # repo root. Running it inside the directory with diff.relative makes
    # git report paths the same way.
    export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=diff.relative GIT_CONFIG_VALUE_0=true
    if [ -z "$(cd "$dir" && git diff --merge-base "$base" --name-only -- '*.go')" ]; then
        echo "git reported no changes inside $dir, so scoping is broken." >&2
        exit 1
    fi
    unset GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0

    # Exclusions match paths relative to the scanned directory.
    prefix=""
    [ "$dir" = "." ] || prefix="$dir/"
    db_pattern="^internal/db/(db|models|querier)\.go$"
    case "internal/db/" in
    "$prefix"*) db_pattern="^${db_pattern#^"$prefix"}" ;;
    esac

    out="$run_dir/report-${#reports[@]}.json"
    echo
    echo "Mutating changed lines in $dir"
    gremlins_status=0
    (cd "$dir" &&
        GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=diff.relative GIT_CONFIG_VALUE_0=true \
            GOFLAGS=-count=1 \
            "$run_dir/bin/gremlins" unleash --config "$repo_root/$config" --diff "$base" --output "$out" \
            --exclude-files '\.sql\.go$' --exclude-files '(^|/)testdata/' --exclude-files "$db_pattern") ||
        gremlins_status=$?
    if [ "$gremlins_status" -ne 0 ]; then
        echo "gremlins exited with status $gremlins_status in $dir." >&2
        exit "$gremlins_status"
    fi
    if [ ! -s "$out" ]; then
        echo "gremlins wrote no report for $dir; the run was probably interrupted." >&2
        exit 1
    fi
    # Report file names relative to the repo root again.
    jq --arg prefix "$prefix" '.files |= map(.file_name = $prefix + .file_name)' "$out" >"$out.rooted"
    reports+=("$out.rooted")
done
jq -s '{files: (map(.files // []) | add)}' "${reports[@]}" >"$report"

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
    classified=$(printf '%s\n' "$survivors" | go run "$(cd "$(dirname "$0")" && pwd)/mutation-const")
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

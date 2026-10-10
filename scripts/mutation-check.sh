#!/usr/bin/env bash

set -euo pipefail

# Usage: mutation-check.sh [base-ref] [dir...]
# Directories, when given, limit mutation to changed lines under them.
base="${1:-origin/main}"
shift || true
scope=()
for dir in "$@"; do
    dir="${dir%/...}"
    dir="${dir#./}"
    dir="${dir%/}"
    if [ ! -d "$dir" ]; then
        echo "Scope '$dir' is not a directory." >&2
        exit 1
    fi
    scope+=("${dir:-.}")
done

gremlins_version="v0.6.0"
config=".gremlins.yaml"
legacy_tmp="${TMPDIR:-/tmp}"
legacy_tmp="${legacy_tmp%/}"
# Tests under gremlins inherit the run's TMPDIR, and macOS caps Unix socket
# paths at 104 bytes, which nesting under macOS's long per-user TMPDIR
# exceeds. /tmp keeps those paths short.
tmp_root="/tmp"

# Every mutant is new source, so its build output is never reused, and
# gremlins builds in copies of the module whose paths differ from the
# checkout's. Left in the shared Go build cache, which only evicts entries
# unused for five days, that output fills the disk. Each run therefore gets
# its own cache and temp directory, deleted on exit.
#
# Runs killed before their EXIT trap fires leave their directory behind, so
# sweep those first. Skip directories whose run is still alive, and ones too
# new to have written their pid yet.
for stale in "$tmp_root"/anvil-mut.*; do
    [ -d "$stale" ] || continue
    pid=$(cat "$stale/pid" 2>/dev/null || true)
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        continue
    fi
    if [ -z "$pid" ] && [ -z "$(find "$stale" -maxdepth 0 -mmin +1)" ]; then
        continue
    fi
    rm -rf "$stale"
done
# Runs from before this isolation left gremlins work directories straight in
# the temp directory. No run lasts a day, so older ones are abandoned.
find "$legacy_tmp" -maxdepth 1 -type d -name 'gremlins-*' -mmin +1440 -exec rm -rf {} + 2>/dev/null || true

run_dir="$(mktemp -d "$tmp_root/anvil-mut.XXXXXX")"
echo $$ >"$run_dir/pid"
trap 'rm -rf "$run_dir"' EXIT
mkdir "$run_dir/tmp"
report="$run_dir/report.json"
scoped_config="$run_dir/gremlins.yaml"

if ! git rev-parse --verify --quiet "${base}^{commit}" >/dev/null; then
    echo "Base ref '$base' not found. Fetch it first (CI needs fetch-depth: 0)." >&2
    exit 1
fi

pathspecs=('*.go')
if [ "${#scope[@]}" -gt 0 ]; then
    pathspecs=()
    for dir in "${scope[@]}"; do
        if [ "$dir" = "." ]; then
            pathspecs+=('*.go')
        else
            pathspecs+=(":(glob)$dir/**/*.go")
        fi
    done
fi

added_lines=$(git diff --merge-base "$base" -U0 -- "${pathspecs[@]}" ':(exclude)*_test.go' |
    grep -E '^\+' | grep -cv '^+++ ' || true)
if [ "$added_lines" -eq 0 ]; then
    echo "No changed Go lines outside tests since $base${scope:+ under ${scope[*]}}; nothing to mutate."
    exit 0
fi
echo "Mutating $added_lines changed Go lines since $base${scope:+ under ${scope[*]}}."

# Gremlins matches its diff against paths relative to the directory it runs
# in, so pointing it at a subdirectory mutates nothing. Run it on the whole
# module instead and exclude every package directory outside the scope.
if [ "${#scope[@]}" -gt 0 ]; then
    alternation=$(git ls-files '*.go' | xargs -n1 dirname | sort -u |
        while IFS= read -r dir; do
            for want in "${scope[@]}"; do
                if [ "$want" = "." ] || [ "$dir" = "$want" ] || [[ "$dir" == "$want"/* ]]; then
                    continue 2
                fi
            done
            if [ "$dir" = "." ]; then
                printf '%s\n' '[^/]+'
            else
                printf '%s/[^/]+\n' "$(printf '%s' "$dir" | sed 's/[][\.*^$()+?{}|]/\\&/g')"
            fi
        done | paste -sd '|' -)
    if [ -n "$alternation" ]; then
        SCOPE_EXCLUDE="^($alternation)\$" awk '
            { print }
            /^  exclude-files:$/ { print "    - '\''" ENVIRON["SCOPE_EXCLUDE"] "'\''"; found = 1 }
            END { exit !found }
        ' "$config" >"$scoped_config" || {
            echo "No unleash.exclude-files list in $config to extend." >&2
            exit 1
        }
        config="$scoped_config"
    fi
fi

# Build gremlins itself with the shared cache, where it is reused across runs.
GOBIN="$run_dir/bin" go install "github.com/go-gremlins/gremlins/cmd/gremlins@${gremlins_version}"

# By default gremlins starts one worker per CPU and each worker's go test uses
# every CPU too, which saturates a laptop. Keep a quarter of the CPUs busy
# with two cores per worker unless overridden; 0 restores gremlins' defaults.
cpus=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)
workers="${MUTATION_WORKERS:-$((cpus / 4 > 0 ? cpus / 4 : 1))}"
test_procs="${MUTATION_GOMAXPROCS:-2}"
if [ "$test_procs" -ne 0 ]; then
    export GOMAXPROCS="$test_procs"
    export GOFLAGS="${GOFLAGS:+$GOFLAGS }-p=$test_procs"
fi

gremlins_status=0
GOCACHE="$run_dir/gocache" TMPDIR="$run_dir/tmp" nice -n "${MUTATION_NICE:-10}" "$run_dir/bin/gremlins" \
    unleash --config "$config" --workers "$workers" --diff "$base" --output "$report" . ||
    gremlins_status=$?
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

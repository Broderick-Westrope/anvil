#!/usr/bin/env bash
# Compares two builds of internal/mermaidbench's tests, A (before a change
# to the Mermaid pipeline) and B (after), run interleaved on one machine so
# drift doesn't line up with the split. Build each with
# `go test -c -o <file> ./internal/mermaidbench`, A at the commit before the
# change but with the harness as it is at B, its `pipeline` import pointed
# at the package A has, so the two differ only in what they measure.
#
# Usage: scripts/mermaidbench-compare.sh <A test binary> <B test binary> <pele-go corpus dir> [runs]
#
# It prints benchstat's comparison and fails if the two builds fail
# different corpus sources, or if B wrote new entries to the shared
# compilation cache. A load that fails and falls back to compiling writes
# nothing, so also check NewRasterizer's medians match: a compile takes
# about 300 ms against about 10 ms for a load. It doesn't apply
# the latency or memory pass bars; read those off benchstat's output. With
# a dozen metrics, one at p < 0.05 with a small delta is likely noise.
set -euo pipefail

if (($# < 3)); then
	echo "usage: mermaidbench-compare.sh <A test binary> <B test binary> <corpus dir> [runs]" >&2
	exit 2
fi
a="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
b="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
export PELE_CORPUS="$3"
runs="${4:-8}"
if ! [[ $runs =~ ^[1-9][0-9]*$ ]]; then
	echo "runs must be a positive integer, not $runs" >&2
	exit 2
fi
benchstat=(go run golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e)
# Fetch benchstat before measuring, so a failure doesn't waste the runs.
"${benchstat[@]}" -h >/dev/null 2>&1 || {
	echo "can't run benchstat: ${benchstat[*]}" >&2
	exit 1
}

out="$(mktemp -d)"
MERMAIDBENCH_CACHE="$(mktemp -d)"
export MERMAIDBENCH_CACHE
trap 'rm -rf "$MERMAIDBENCH_CACHE"; echo "raw results: $out" >&2' EXIT

entries() { find "$MERMAIDBENCH_CACHE" -type f | sort; }

# run_test runs one test of one build into a log, and shows the log if it
# fails.
run_test() {
	local bin="$1" test="$2" log="$3"
	if ! "$bin" -test.run "^$test\$" >"$log" 2>&1; then
		echo "FAIL: $bin $test:" >&2
		cat "$log" >&2
		exit 1
	fi
}

# A fills the cache, as an earlier Anvil session would have. B's first
# load comes after the snapshot, so a miss shows as a new entry. Running B
# once before measuring also means neither build's first measured run is
# the one that pages its binary in.
run_test "$a" TestMemory "$out/warm-a.log"
before="$(entries)"
run_test "$b" TestMemory "$out/warm-b.log"

# Alternate ABBA so whichever goes first in a pair isn't always the same
# build.
for i in $(seq "$runs"); do
	order="a b"
	((i % 2 == 0)) && order="b a"
	for build in $order; do
		bin="$a"
		[[ $build == b ]] && bin="$b"
		for test in TestLatency TestMemory; do
			log="$out/$build-$test-$i.log"
			run_test "$bin" "$test" "$log"
			if ! grep -E '^(Benchmark|# )' "$log" >>"$out/$build.txt"; then
				echo "FAIL: no benchmark lines in $log; did the test skip?" >&2
				exit 1
			fi
		done
	done
	echo "run $i of $runs done" >&2
done

status=0
if [[ "$(entries)" != "$before" ]]; then
	echo "FAIL: the compilation cache gained entries; B did not load A's compiled resvg" >&2
	status=1
fi
fa="$(grep '^# corpus' "$out/a.txt" | sort -u)"
fb="$(grep '^# corpus' "$out/b.txt" | sort -u)"
echo "A: $fa"
echo "B: $fb"
if [[ "$fa" != "$fb" ]]; then
	echo "FAIL: A and B fail different corpus sources, or fail them differently" >&2
	status=1
fi
"${benchstat[@]}" "$out/a.txt" "$out/b.txt"
exit "$status"

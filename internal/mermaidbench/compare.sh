#!/usr/bin/env bash
# Compares two builds of this package's tests, A (before a change) and B
# (after), run interleaved on one machine so drift doesn't line up with the
# split. Build each with `go test -c -o <file> ./internal/mermaidbench`.
#
# Usage: compare.sh <A test binary> <B test binary> <pele-go corpus dir> [runs]
#
# It prints benchstat's comparison and fails if the two builds fail
# different corpus sources, or if B compiled resvg again instead of loading
# A's compiled code from the shared cache.
set -euo pipefail

a="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
b="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"
export PELE_CORPUS="${3:?usage: compare.sh <A> <B> <corpus dir> [runs]}"
runs="${4:-8}"
benchstat=(go run golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e)

out="$(mktemp -d)"
MERMAIDBENCH_CACHE="$(mktemp -d)"
export MERMAIDBENCH_CACHE
trap 'rm -rf "$MERMAIDBENCH_CACHE"' EXIT

entries() { find "$MERMAIDBENCH_CACHE" -type f | sort; }

# A fills the cache, as an earlier Anvil session would have.
"$a" -test.run '^TestMemory$' >/dev/null
before="$(entries)"

for i in $(seq "$runs"); do
	for build in a b; do
		bin="$a"
		[[ $build == b ]] && bin="$b"
		"$bin" -test.run '^TestLatency$' | grep -E '^(Benchmark|# )' >>"$out/$build.txt"
		"$bin" -test.run '^TestMemory$' | grep '^Benchmark' >>"$out/$build.txt"
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
echo "raw results: $out" >&2
exit "$status"

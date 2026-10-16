#!/usr/bin/env bash
# Checks scripts/ci-code-changed.sh against fixed `git diff --raw` inputs.
# The classify job runs it before classifying, on the same Ubuntu runner
# and bash that run the classifier, so a broken classifier fails that job
# instead of skipping checks it shouldn't.

set -euo pipefail

classifier="$(dirname "$0")/ci-code-changed.sh"
failed=0

check() {
    local want=$1 name=$2 input=$3 got
    got=$(printf '%b' "$input" | "$classifier")
    if [ "$got" != "$want" ]; then
        echo "FAIL: $name: want $want, got $got" >&2
        failed=1
    fi
}

mod=':100644 100644 aaaaaaa bbbbbbb M'
add=':000000 100644 0000000 bbbbbbb A'
del=':100644 000000 aaaaaaa 0000000 D'

check false "root markdown" "$mod\tAGENTS.md\n"
check false "plans and docs markdown" "$add\tplans/x.md\n$mod\tdocs/adr/0004-x.md\n$del\tREADME.md\n"
check true "no input" ""
check true "blank line" "\n"
check true "markdown in internal is embedded" "$mod\tAGENTS.md\n$mod\tinternal/agent/tools/bash.md.tpl\n"
check true "builtin skill" "$mod\tinternal/skills/builtin/jq/SKILL.md\n"
check true "go file under docs" "$add\tdocs/probe_test.go\n"
check true "script under docs" "$mod\tdocs/hooks/examples/rtk-rewrite.sh\n"
check true "image under docs" "$add\tdocs/hooks/diagram.png\n"
check true "go module" "$mod\tgo.mod\n"
check true "workflow" "$mod\t.github/workflows/build.yml\n"
check true "root non-markdown" "$mod\tTaskfile.yaml\n"
check true "lookalike directory" "$add\tplansx/a.md\n"
check true "code with prose" "$mod\tinternal/x.go\n$mod\tplans/y.md\n"
check true "executable markdown" ":100644 100755 aaaaaaa aaaaaaa M\tdocs/x.md\n"
check true "symlink markdown" ":000000 120000 0000000 bbbbbbb A\tdocs/x.md\n"
check true "submodule" ":000000 160000 0000000 bbbbbbb A\tdocs/vendor.md\n"
check true "quoted path" "$add\t\"docs/caf\\\\303\\\\251.md\"\n"
check true "directory named like markdown" "$add\tdocs/x.md/y.go\n"
check false "nested plans markdown" "$add\tplans/impl/phase-1.md\n"
check true "uppercase extension" "$add\tdocs/x.MD\n"
check true "markdown-like extension" "$add\tdocs/x.md.txt\n"
check false "root dotfile markdown" "$add\t.notes.md\n"
check true "type change" ":100644 120000 aaaaaaa bbbbbbb T\tdocs/x.md\n"
check true "rename" ":100644 100644 aaaaaaa bbbbbbb R100\tplans/x.go\tdocs/x.md\n"
check true "copy" ":100644 100644 aaaaaaa bbbbbbb C100\tplans/x.go\tdocs/x.md\n"
check true "code on a last line without a newline" "$mod\tdocs/a.md\n$mod\tmain.go"
check false "prose on a last line without a newline" "$mod\tdocs/a.md"

if [ "$failed" -ne 0 ]; then
    exit 1
fi
echo "ci-code-changed: all cases pass"

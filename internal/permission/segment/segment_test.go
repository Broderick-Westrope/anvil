package segment

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/permission/match"
)

func TestIsRedirect(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"> out", ">> out", "2> out", "&> out", "&>> out", ">| out", "<> out"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			require.True(t, IsRedirect("  "+input))
			require.Equal(t, input, Generalize(input))
		})
	}
	require.False(t, IsRedirect("git status"))
}

func TestSplit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "simple command",
			command: "git status",
			want:    []string{"git status"},
		},
		{
			name:    "and chain",
			command: "cd /foo && go test ./...",
			want:    []string{"cd /foo", "go test ./..."},
		},
		{
			name:    "pipe",
			command: "go test ./... | tail -20",
			want:    []string{"go test ./...", "tail -20"},
		},
		{
			name:    "semicolon",
			command: "ls; pwd",
			want:    []string{"ls", "pwd"},
		},
		{
			name:    "or chain",
			command: "false || echo hi",
			want:    []string{"false", "echo hi"},
		},
		{
			name:    "env prefix kept as an extra segment",
			command: "FOO=bar go test",
			want:    []string{"go test", "FOO=bar go test"},
		},
		{
			name:    "pure assignment is a segment",
			command: "FOO=bar",
			want:    []string{"FOO=bar"},
		},
		{
			name:    "fd duplication stays inline",
			command: "go test ./... 2>&1",
			want:    []string{"go test ./... 2>&1"},
		},
		{
			name:    "input redirect stays inline",
			command: "cat < /etc/passwd",
			want:    []string{"cat </etc/passwd"},
		},
		{
			name:    "output redirect is its own segment",
			command: "ls > /etc/passwd",
			want:    []string{"ls", "> /etc/passwd"},
		},
		{
			name:    "append redirect is its own segment",
			command: "printf x >> ~/.ssh/authorized_keys",
			want:    []string{"printf x", ">> ~/.ssh/authorized_keys"},
		},
		{
			name:    "numbered write redirect keeps its fd",
			command: "ls 2> /tmp/err",
			want:    []string{"ls", "2> /tmp/err"},
		},
		{
			name:    "clobber redirect is a write",
			command: "ls >| /etc/passwd",
			want:    []string{"ls", ">| /etc/passwd"},
		},
		{
			name:    "all-streams redirect is a write",
			command: "ls &> /etc/passwd",
			want:    []string{"ls", "&> /etc/passwd"},
		},
		{
			name:    "dup-out to a file is a write",
			command: "ls >& /etc/passwd",
			want:    []string{"ls", ">& /etc/passwd"},
		},
		{
			name:    "multiple writes each get a segment",
			command: "ls > /tmp/a > /tmp/b",
			want:    []string{"ls", "> /tmp/a", "> /tmp/b"},
		},
		{
			name:    "write hidden behind a subshell is surfaced",
			command: "(ls) > /etc/passwd",
			want:    []string{"> /etc/passwd", "ls"},
		},
		{
			name:    "wrapper target is its own segment",
			command: "env rm -rf /tmp/x",
			want:    []string{"env rm -rf /tmp/x", "rm -rf /tmp/x"},
		},
		{
			name:    "wrapper skips assignments",
			command: "env FOO=bar go test ./...",
			want:    []string{"env FOO=bar go test ./...", "go test ./...", "FOO=bar go test ./..."},
		},
		{
			name:    "wrapper skips flags",
			command: "env -i rm -rf /",
			want:    []string{"env -i rm -rf /", "rm -rf /"},
		},
		{
			name:    "wrapper skips numeric operands",
			command: "timeout 5 rm -rf /",
			want:    []string{"timeout 5 rm -rf /", "rm -rf /"},
		},
		{
			name:    "nested wrappers resolve to the innermost target",
			command: "sudo env bash -c 'rm -rf ~'",
			want:    []string{"sudo env bash -c 'rm -rf ~'", "sudo env bash -c rm -rf ~", "bash -c 'rm -rf ~'", "bash -c rm -rf ~", "rm -rf ~"},
		},
		{
			name:    "xargs target is its own segment",
			command: "cat files.txt | xargs rm",
			want:    []string{"cat files.txt", "xargs rm", "rm"},
		},
		{
			name:    "find exec body is its own segment",
			command: `find . -name '*.go' -exec rm {} \;`,
			want:    []string{`find . -name '*.go' -exec rm {} \;`, "find . -name *.go -exec rm {} ;", "rm {}"},
		},
		{
			name:    "find execdir body is its own segment",
			command: `find . -execdir sh -c 'echo hi' \;`,
			want:    []string{`find . -execdir sh -c 'echo hi' \;`, "find . -execdir sh -c echo hi ;", "sh -c 'echo hi'", "sh -c echo hi"},
		},
		{
			name:    "find exec terminated by plus",
			command: "find . -exec rm {} +",
			want:    []string{"find . -exec rm {} +", "rm {}"},
		},
		{
			name:    "command substitution",
			command: "echo $(date)",
			want:    []string{"echo $(date)", "date"},
		},
		{
			name:    "subshell",
			command: "(cd /tmp && ls)",
			want:    []string{"cd /tmp", "ls"},
		},
		{
			name:    "quoted strings preserved",
			command: `git commit -m "hello world"`,
			want:    []string{`git commit -m "hello world"`, "git commit -m hello world"},
		},
		{
			name:    "complex real-world",
			command: "cd /foo && GOFLAGS=-count=1 go test ./internal/... 2>&1 | tail -20",
			want:    []string{"cd /foo", "go test ./internal/... 2>&1", "GOFLAGS=-count=1 go test ./internal/... 2>&1", "tail -20"},
		},
		{
			name:    "process substitution traversed",
			command: "diff <(ls) <(ls /tmp)",
			want:    []string{"diff <(ls) <(ls /tmp)", "ls", "ls /tmp"},
		},
		{
			name:    "loop body traversed",
			command: "while true; do rm -rf /; done",
			want:    []string{"true", "rm -rf /"},
		},
		{
			name:    "parse error fallback",
			command: "if then fi (",
			want:    []string{"if then fi ("},
		},
		{
			name:    "single-quoted command name is normalised",
			command: "'rm' -rf x",
			want:    []string{"'rm' -rf x", "rm -rf x"},
		},
		{
			name:    "double-quoted command name is normalised",
			command: `"rm" -rf x`,
			want:    []string{`"rm" -rf x`, "rm -rf x"},
		},
		{
			name:    "backslash-escaped command name is normalised",
			command: `\rm -rf x`,
			want:    []string{`\rm -rf x`, "rm -rf x"},
		},
		{
			name:    "empty quotes inside a command name are removed",
			command: "r''m -rf x",
			want:    []string{"r''m -rf x", "rm -rf x"},
		},
		{
			name:    "ANSI-C quoted command name is decoded",
			command: `$'\x72m' -rf x`,
			want:    []string{`$'\x72m' -rf x`, "rm -rf x"},
		},
		{
			name:    "ANSI-C control characters become spaces",
			command: `echo $'a\tb\nc'`,
			want:    []string{`echo $'a\tb\nc'`, "echo a b c"},
		},
		{
			name:    "command path is reduced to its base name",
			command: "/bin/rm -rf x",
			want:    []string{"/bin/rm -rf x", "rm -rf x"},
		},
		{
			name:    "relative command path is reduced to its base name",
			command: "./rm -rf x",
			want:    []string{"./rm -rf x", "rm -rf x"},
		},
		{
			name:    "quoted flag is normalised",
			command: "git push '--force' origin",
			want:    []string{"git push '--force' origin", "git push --force origin"},
		},
		{
			name:    "brace expansion in command name is expanded",
			command: "{rm,-rf} x",
			want:    []string{"{rm,-rf} x", "rm -rf x"},
		},
		{
			name:    "quoted wrapper is unwrapped",
			command: "'sudo' 'rm' -rf x",
			want:    []string{"'sudo' 'rm' -rf x", "sudo rm -rf x", "rm -rf x"},
		},
		{
			name:    "wrapper path is unwrapped",
			command: "/usr/bin/env rm -rf x",
			want:    []string{"/usr/bin/env rm -rf x", "env rm -rf x", "rm -rf x"},
		},
		{
			name:    "quoted write target is normalised",
			command: "ls > '/etc/passwd'",
			want:    []string{"ls", "> '/etc/passwd'", "> /etc/passwd"},
		},
		{
			name:    "variable command name keeps its printed form",
			command: "$cmd -rf x",
			want:    []string{"$cmd -rf x"},
		},
		{
			name:    "substituted command name keeps its printed form",
			command: "$(echo rm) -rf x",
			want:    []string{"$(echo rm) -rf x", "echo rm"},
		},
		{
			name:    "double-quoted variable keeps its printed form",
			command: `rm "$dir"`,
			want:    []string{`rm "$dir"`},
		},
		{
			name:    "script payloads unwrap up to the depth limit",
			command: strings.Repeat("eval ", 8) + "rm -rf x",
			want: []string{
				strings.Repeat("eval ", 8) + "rm -rf x",
				strings.Repeat("eval ", 7) + "rm -rf x",
				strings.Repeat("eval ", 6) + "rm -rf x",
				strings.Repeat("eval ", 5) + "rm -rf x",
				strings.Repeat("eval ", 4) + "rm -rf x",
				strings.Repeat("eval ", 3) + "rm -rf x",
				strings.Repeat("eval ", 2) + "rm -rf x",
				strings.Repeat("eval ", 1) + "rm -rf x",
				"rm -rf x",
			},
		},
		{
			name:    "script payloads past the depth limit stay wrapped",
			command: strings.Repeat("eval ", 9) + "rm -rf x",
			want: []string{
				strings.Repeat("eval ", 9) + "rm -rf x",
				strings.Repeat("eval ", 8) + "rm -rf x",
				strings.Repeat("eval ", 7) + "rm -rf x",
				strings.Repeat("eval ", 6) + "rm -rf x",
				strings.Repeat("eval ", 5) + "rm -rf x",
				strings.Repeat("eval ", 4) + "rm -rf x",
				strings.Repeat("eval ", 3) + "rm -rf x",
				strings.Repeat("eval ", 2) + "rm -rf x",
				strings.Repeat("eval ", 1) + "rm -rf x",
			},
		},
		{
			name:    "duplicates deduped",
			command: "ls && ls",
			want:    []string{"ls"},
		},
		{
			name:    "empty string",
			command: "",
			want:    []string{""},
		},
		{
			name:    "whitespace only",
			command: "   ",
			want:    []string{"   "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Split(tt.command))
		})
	}
}

func TestSplitBraceExpansionIsBounded(t *testing.T) {
	t.Parallel()
	bomb := "echo " + strings.Repeat("{a,b}", 40)
	done := make(chan []string, 1)
	go func() { done <- Split(bomb) }()
	select {
	case segs := <-done:
		require.Equal(t, []string{bomb}, segs)
	case <-time.After(2 * time.Second):
		t.Fatal("brace expansion was not bounded")
	}
	require.Equal(t, []string{"{1..100000} x"}, Split("{1..100000} x"))
	require.Equal(t, []string{"{rm,x} y", "rm x y"}, Split("{rm,x} y"))
}

func TestSplitScriptPayloads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		want    string
	}{
		{"bash -c 'rm -rf x'", "rm -rf x"},
		{"sh -ec 'rm -rf x'", "rm -rf x"},
		{"/bin/zsh -c 'rm -rf x'", "rm -rf x"},
		{"eval 'rm -rf x'", "rm -rf x"},
		{"eval rm -rf x", "rm -rf x"},
		{"env -S 'rm -rf x'", "rm -rf x"},
		{"sudo -u root bash -c 'rm -rf x'", "rm -rf x"},
		{"bash -c 'ls; rm -rf x'", "rm -rf x"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			require.Contains(t, Split(tt.command), tt.want)
			require.Contains(t, Normalized(tt.command), tt.want)
		})
	}
	require.NotContains(t, Split("bash script.sh -c 'rm -rf x'"), "rm -rf x")
}

func TestUnwrapValueFlags(t *testing.T) {
	t.Parallel()
	for command, want := range map[string]string{
		"sudo -u root rm -rf x":      "rm -rf x",
		"sudo --user root rm -rf x":  "rm -rf x",
		"env -u HOME rm -rf x":       "rm -rf x",
		"env -- rm -rf x":            "rm -rf x",
		"timeout -s KILL 5 rm -rf x": "rm -rf x",
		"nice -n 10 rm -rf x":        "rm -rf x",
		"xargs -I {} rm {}":          "rm {}",
		"stdbuf -o L rm -rf x":       "rm -rf x",
	} {
		require.Contains(t, Split(command), want, command)
	}
}

func TestNormalized(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		want    []string
	}{
		{"git status", []string{"git status"}},
		{`git commit -m "hello world"`, []string{"git commit -m hello world"}},
		{"'rm' -rf x && /bin/ls", []string{"rm -rf x", "ls"}},
		{"ls > '/tmp/out'", []string{"ls", "> /tmp/out"}},
		{"sudo 'rm' x", []string{"sudo rm x", "rm x"}},
		{"FOO=bar", []string{"FOO=bar"}},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Normalized(tt.command))
		})
	}
}

func TestGeneralize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		segment string
		want    string
	}{
		{
			name:    "git commit with args",
			segment: "git commit -m foo",
			want:    "git commit *",
		},
		{
			name:    "go test",
			segment: "go test ./...",
			want:    "go test *",
		},
		{
			name:    "leading assignments are kept verbatim",
			segment: "LD_PRELOAD=/tmp/x.so ls -la",
			want:    "LD_PRELOAD=/tmp/x.so ls *",
		},
		{
			name:    "several assignments",
			segment: "GOOS=linux GOARCH=arm64 go build ./...",
			want:    "GOOS=linux GOARCH=arm64 go build *",
		},
		{
			name:    "pure assignment",
			segment: "PATH=/tmp/evil",
			want:    "PATH=/tmp/evil",
		},
		{
			name:    "bare export",
			segment: "export",
			want:    "export *",
		},
		{
			name:    "assignment-shaped argument of another command",
			segment: "git FOO=bar",
			want:    "git *",
		},
		{
			name:    "export is kept verbatim",
			segment: "export LD_PRELOAD=/tmp/x.so",
			want:    "export LD_PRELOAD=/tmp/x.so",
		},
		{
			name:    "flags are not subcommands",
			segment: "ls -la /tmp",
			want:    "ls *",
		},
		{
			name:    "files are not subcommands",
			segment: "cat file.txt",
			want:    "cat *",
		},
		{
			name:    "single token",
			segment: "pwd",
			want:    "pwd *",
		},
		{
			name:    "hyphenated subcommand",
			segment: "git rev-parse HEAD",
			want:    "git rev-parse *",
		},
		{
			name:    "npm run",
			segment: "npm run build",
			want:    "npm run *",
		},
		{
			name:    "empty string",
			segment: "",
			want:    "",
		},
		{
			name:    "write redirect is not generalized",
			segment: "> /tmp/out.txt",
			want:    "> /tmp/out.txt",
		},
		{
			name:    "append redirect is not generalized",
			segment: ">> ~/.zshrc",
			want:    ">> ~/.zshrc",
		},
		{
			name:    "numbered redirect is not generalized",
			segment: "2> /tmp/err",
			want:    "2> /tmp/err",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Generalize(tt.segment))
		})
	}
}

func TestGeneralizeMatchesSourceSegment(t *testing.T) {
	t.Parallel()

	segments := []string{
		"git commit -m foo",
		"go test ./...",
		"ls -la /tmp",
		"cat file.txt",
		"pwd",
		"git rev-parse HEAD",
		"npm run build",
		"git status",
		"> /tmp/out.txt",
		">> /tmp/out.txt",
		"2> /tmp/err",
		"LD_PRELOAD=/tmp/x.so ls -la",
		"GOOS=linux GOARCH=arm64 go build ./...",
		"PATH=/tmp/evil",
		"export LD_PRELOAD=/tmp/x.so",
	}

	for _, seg := range segments {
		t.Run(seg, func(t *testing.T) {
			t.Parallel()
			pattern := Generalize(seg)
			ok, err := match.Match(pattern, seg)
			require.NoError(t, err)
			require.True(t, ok, "pattern %q should match segment %q", pattern, seg)
		})
	}
}

func TestGeneralizedAssignmentDoesNotCoverOtherVariables(t *testing.T) {
	t.Parallel()

	pattern := Generalize("GOOS=linux go build ./...")
	for _, seg := range []string{
		"GOOS=linux LD_PRELOAD=/tmp/x.so go build ./...",
		"GOOS=darwin go build ./...",
		"go build ./...",
	} {
		ok, err := match.Match(pattern, seg)
		require.NoError(t, err)
		require.False(t, ok, "pattern %q must not match %q", pattern, seg)
	}
}

func TestTrailingStarMatchesBarePrefix(t *testing.T) {
	t.Parallel()

	ok, err := match.Match("git status *", "git status")
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = match.Match("git status *", "git statusfoo")
	require.NoError(t, err)
	require.False(t, ok)

	ok, err = match.Match("git status *", "git status --short")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSplitKeepsEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		want    []string
	}{
		{"LD_PRELOAD=/x.so ls", []string{"ls", "LD_PRELOAD=/x.so ls"}},
		{"env LD_PRELOAD=/x.so ls", []string{"env LD_PRELOAD=/x.so ls", "ls", "LD_PRELOAD=/x.so ls"}},
		{"env FOO=1 timeout 5 BAR=2 ls", []string{"env FOO=1 timeout 5 BAR=2 ls", "ls", "FOO=1 BAR=2 ls"}},
		{"sudo LD_PRELOAD=/x ls", []string{"sudo LD_PRELOAD=/x ls", "ls", "LD_PRELOAD=/x ls"}},
		{"export LD_PRELOAD=/x.so; ls", []string{"export LD_PRELOAD=/x.so", "ls"}},
		{"declare -x FOO=bar && ls", []string{"declare -x FOO=bar", "ls"}},
		{"(FOO=bar ls)", []string{"ls", "FOO=bar ls"}},
		{"FOO='a b' ls", []string{"ls", "FOO='a b' ls", "FOO=a b ls"}},
		{"PATH=/evil:$PATH ls", []string{"ls", "PATH=/evil:$PATH ls"}},
		{"FOO= ls", []string{"ls", "FOO= ls"}},
		{"FOO+=bar ls", []string{"ls", "FOO+=bar ls"}},
		{"a[1]=x", []string{"a[1]=x"}},
		{"FOO='' ls", []string{"ls", "FOO='' ls", "FOO= ls"}},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Split(tt.command))
		})
	}
}

// TestSplitUnwrapDepthLimit pins where wrapper unwrapping stops: eight
// nested wrappers resolve to their target, and a ninth is left unresolved,
// so its target is never offered on its own.
func TestSplitUnwrapDepthLimit(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{strings.Repeat("env ", 8) + "ls", "ls"},
		Split(strings.Repeat("env ", 8)+"ls"))
	require.Equal(t,
		[]string{strings.Repeat("env ", 9) + "ls", "env ls"},
		Split(strings.Repeat("env ", 9)+"ls"))
}

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
			name:    "env prefix stripped",
			command: "FOO=bar go test",
			want:    []string{"go test"},
		},
		{
			name:    "pure assignment skipped",
			command: "FOO=bar",
			want:    nil,
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
			want:    []string{"env FOO=bar go test ./...", "go test ./..."},
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
			want:    []string{"cd /foo", "go test ./internal/... 2>&1", "tail -20"},
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
		{"FOO=bar", nil},
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

package segment

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/permission/match"
)

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
			name:    "redirect kept visible",
			command: "go test ./... 2>&1",
			want:    []string{"go test ./... 2>&1"},
		},
		{
			name:    "output redirect kept visible",
			command: "ls > /etc/passwd",
			want:    []string{"ls >/etc/passwd"},
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
			want:    []string{`git commit -m "hello world"`},
		},
		{
			name:    "complex real-world",
			command: "cd /foo && GOFLAGS=-count=1 go test ./internal/... 2>&1 | tail -20",
			want:    []string{"cd /foo", "go test ./internal/... 2>&1", "tail -20"},
		},
		{
			name:    "parse error fallback",
			command: "if then fi (",
			want:    []string{"if then fi ("},
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

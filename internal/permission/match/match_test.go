package match

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		// Basic glob.
		{name: "star matches everything", pattern: "*", input: "anything", want: true},
		{name: "star matches empty", pattern: "*", input: "", want: true},
		{name: "git star matches git status", pattern: "git *", input: "git status", want: true},
		{name: "git star does not match npm install", pattern: "git *", input: "npm install", want: false},
		{name: "question mark matches single char", pattern: "tes?", input: "test", want: true},
		{name: "question mark does not match empty", pattern: "tes?", input: "tes", want: false},

		// Brace expansion.
		{name: "brace matches first", pattern: "{edit,write}", input: "edit", want: true},
		{name: "brace matches second", pattern: "{edit,write}", input: "write", want: true},
		{name: "brace does not match other", pattern: "{edit,write}", input: "view", want: false},

		// Combined glob and brace.
		{name: "combined matches exact", pattern: "{edit,multi*}", input: "edit", want: true},
		{name: "combined matches glob", pattern: "{edit,multi*}", input: "multiedit", want: true},
		{name: "combined matches glob 2", pattern: "{edit,multi*}", input: "multiwrite", want: true},
		{name: "combined does not match other", pattern: "{edit,multi*}", input: "view", want: false},

		// Nested braces.
		{name: "nested matches a", pattern: "{a,{b,c}}", input: "a", want: true},
		{name: "nested matches b", pattern: "{a,{b,c}}", input: "b", want: true},
		{name: "nested matches c", pattern: "{a,{b,c}}", input: "c", want: true},
		{name: "nested does not match d", pattern: "{a,{b,c}}", input: "d", want: false},

		// File path patterns. The * wildcard crosses '/' so patterns
		// match nested paths and bash commands containing paths.
		{name: "path matches file", pattern: "internal/*.go", input: "internal/foo.go", want: true},
		{name: "path matches subdir", pattern: "internal/*.go", input: "internal/sub/foo.go", want: true},
		{name: "star crosses slash", pattern: "*", input: "a/b", want: true},
		{name: "bash command with path", pattern: "git diff *", input: "git diff internal/foo.go", want: true},
		{name: "tmp prefix matches nested", pattern: "/tmp/*", input: "/tmp/sub/file.txt", want: true},

		// Exact match (no special chars).
		{name: "exact match", pattern: "bash", input: "bash", want: true},
		{name: "exact no match", pattern: "bash", input: "edit", want: false},

		// Empty pattern and input.
		{name: "empty pattern matches empty input", pattern: "", input: "", want: true},
		{name: "empty pattern does not match nonempty", pattern: "", input: "x", want: false},
		{name: "nonempty pattern does not match empty", pattern: "x", input: "", want: false},

		// Character class.
		{name: "char class match", pattern: "[abc]", input: "a", want: true},
		{name: "char class no match", pattern: "[abc]", input: "d", want: false},

		// Escaped special chars.
		{name: "escaped star is literal", pattern: `\*`, input: "*", want: true},
		{name: "escaped star does not glob", pattern: `\*`, input: "foo", want: false},
		{name: "escaped brace is literal", pattern: `\{a,b\}`, input: "{a,b}", want: true},

		// Prefix with brace.
		{name: "prefix brace", pattern: "cmd-{a,b}", input: "cmd-a", want: true},
		{name: "prefix brace 2", pattern: "cmd-{a,b}", input: "cmd-b", want: true},
		{name: "prefix brace no match", pattern: "cmd-{a,b}", input: "cmd-c", want: false},

		// Suffix with brace.
		{name: "suffix brace", pattern: "{read,write}-file", input: "read-file", want: true},
		{name: "suffix brace no match", pattern: "{read,write}-file", input: "exec-file", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Match(tt.pattern, tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMatch_InvalidPattern(t *testing.T) {
	t.Parallel()

	_, err := Match("{unclosed", "input")
	require.Error(t, err)
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{name: "simple glob", pattern: "*", wantErr: false},
		{name: "brace expansion", pattern: "{a,b}", wantErr: false},
		{name: "nested braces", pattern: "{a,{b,c}}", wantErr: false},
		{name: "char class", pattern: "[abc]", wantErr: false},
		{name: "exact string", pattern: "bash", wantErr: false},
		{name: "empty pattern", pattern: "", wantErr: false},
		{name: "escaped braces", pattern: `\{a\}`, wantErr: false},

		// Invalid patterns.
		{name: "unmatched open brace", pattern: "{a,b", wantErr: true},
		{name: "unmatched close brace", pattern: "a,b}", wantErr: true},
		{name: "invalid char class", pattern: "[", wantErr: true},
		{name: "invalid char class unclosed", pattern: "[abc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tt.pattern)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestExpandBraces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{name: "no braces", pattern: "foo", want: []string{"foo"}},
		{name: "simple", pattern: "{a,b}", want: []string{"a", "b"}},
		{name: "with prefix", pattern: "x{a,b}", want: []string{"xa", "xb"}},
		{name: "with suffix", pattern: "{a,b}y", want: []string{"ay", "by"}},
		{name: "with both", pattern: "x{a,b}y", want: []string{"xay", "xby"}},
		{name: "nested", pattern: "{a,{b,c}}", want: []string{"a", "b", "c"}},
		{name: "three alternatives", pattern: "{a,b,c}", want: []string{"a", "b", "c"}},
		{name: "single alternative", pattern: "{a}", want: []string{"a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := expandBraces(tt.pattern)
			require.Equal(t, tt.want, got)
		})
	}
}

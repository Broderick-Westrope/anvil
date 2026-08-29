package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// oldTrimTrailingSpaces is the original four-pass pipeline used before the
// single-pass trimTrailingSpaces was introduced. It is kept here solely to
// drive equivalence tests.
func oldTrimTrailingSpaces(s string) string {
	content := strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

func TestTrimTrailingSpaces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "no trailing spaces",
			input: "hello\nworld",
			want:  "hello\nworld",
		},
		{
			name:  "trailing spaces on one line",
			input: "hello   \nworld",
			want:  "hello\nworld",
		},
		{
			name:  "trailing spaces on every line",
			input: "foo  \nbar  \nbaz  ",
			want:  "foo\nbar\nbaz",
		},
		{
			name:  "line of only spaces becomes empty",
			input: "   \n",
			want:  "\n",
		},
		{
			name:  "all spaces no newline becomes empty",
			input: "   ",
			want:  "",
		},
		{
			name:  "CRLF normalised to LF",
			input: "foo   \r\nbar  \r\nbaz",
			want:  "foo\nbar\nbaz",
		},
		{
			name:  "CRLF on all-space line",
			input: "   \r\n",
			want:  "\n",
		},
		{
			name:  "mixed CRLF and LF",
			input: "a  \r\nb  \nc  ",
			want:  "a\nb\nc",
		},
		{
			name:  "trailing newline preserved",
			input: "hello\n",
			want:  "hello\n",
		},
		{
			name:  "multiple consecutive blank lines",
			input: "\n\n\n",
			want:  "\n\n\n",
		},
		{
			name:  "spaces before and after content",
			input: "  hello  \n  world  ",
			want:  "  hello\n  world",
		},
		{
			name:  "tab characters not stripped",
			input: "hello\t\n",
			want:  "hello\t\n",
		},
		{
			name:  "single space line with no newline",
			input: " ",
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := trimTrailingSpaces(tc.input)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestTrimTrailingSpacesEquivalence verifies that trimTrailingSpaces produces
// byte-identical output to the original four-pass pipeline for every case.
func TestTrimTrailingSpacesEquivalence(t *testing.T) {
	t.Parallel()

	cases := []string{
		"",
		"foo",
		"foo  ",
		"foo\n",
		"foo  \n",
		"foo  \nbar  ",
		"  \n  \n  ",
		"foo\r\nbar",
		"foo  \r\nbar  \r\nbaz",
		"   \r\n",
		"hello\t\n",
		"\n\n",
		" ",
		"a  \r\nb  \nc  ",
	}

	for _, input := range cases {
		t.Run(strings.ReplaceAll(input, "\n", "\\n"), func(t *testing.T) {
			t.Parallel()
			got := trimTrailingSpaces(input)
			want := oldTrimTrailingSpaces(input)
			require.Equal(t, want, got, "input: %q", input)
		})
	}
}

// BenchmarkTrimTrailingSpaces compares the single-pass implementation against
// the original four-pass pipeline on a realistic frame-sized payload.
func BenchmarkTrimTrailingSpaces(b *testing.B) {
	// Build a ~200-line string with varying trailing spaces to approximate a
	// real terminal frame rendered by canvas.Render().
	var sb strings.Builder
	for i := range 200 {
		switch i % 4 {
		case 0:
			sb.WriteString("hello world              \r\n")
		case 1:
			sb.WriteString("  indented content       \r\n")
		case 2:
			sb.WriteString("no trailing here\r\n")
		case 3:
			sb.WriteString("                         \r\n")
		}
	}
	payload := sb.String()

	b.Run("single-pass", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			_ = trimTrailingSpaces(payload)
		}
	})

	b.Run("four-pass-original", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			_ = oldTrimTrailingSpaces(payload)
		}
	})
}

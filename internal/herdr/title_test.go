package herdr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		want  string
	}{
		{name: "empty", title: "", want: ""},
		{name: "plain", title: "Fix auth", want: "Fix auth"},
		{name: "OSC sequence stripped", title: "a\x1b]2;x\x07b", want: "ab"},
		{name: "CSI sequence stripped", title: "\x1b[31mred\x1b[0m", want: "red"},
		{name: "controls become spaces", title: "Fix\nauth\tnow", want: "Fix auth now"},
		{name: "whitespace collapsed", title: "  Fix \t  auth  ", want: "Fix auth"},
		{name: "only control characters", title: "\n\r\t", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CleanTitle(tt.title)
			require.Equal(t, tt.want, got)
			require.NotContains(t, got, "]2;")
		})
	}
}

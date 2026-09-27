package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrintResumeHint(t *testing.T) {
	t.Parallel()

	t.Run("prints command for active session", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		printResumeHint(&buf, "abc-123")
		require.Equal(t, "Resume this session with:\n  anvil --session abc-123 --there\n", buf.String())
	})

	t.Run("prints nothing without session", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		printResumeHint(&buf, "")
		require.Empty(t, buf.String())
	})
}

package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPrintResumeHint(t *testing.T) {
	t.Parallel()

	t.Run("prints command for active session", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		printResumeHint(&buf, "abc-123", time.Date(2026, 10, 4, 15, 30, 1, 0, time.UTC))
		require.Equal(t, "[2026-10-04 15:30:01 UTC] Resume this session with:\n  anvil --session abc-123 --there\n", buf.String())
	})

	t.Run("prints nothing without session", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		printResumeHint(&buf, "", time.Now())
		require.Empty(t, buf.String())
	})
}

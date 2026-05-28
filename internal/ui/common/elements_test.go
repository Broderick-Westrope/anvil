package common

import (
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		d    time.Duration
		want string
	}{
		"zero seconds":        {d: 0, want: "0s"},
		"14 seconds":          {d: 14 * time.Second, want: "14s"},
		"59 seconds":          {d: 59 * time.Second, want: "59s"},
		"exactly 60 seconds":  {d: 60 * time.Second, want: "1m"},
		"90 seconds":          {d: 90 * time.Second, want: "1m30s"},
		"5 minutes":           {d: 5 * time.Minute, want: "5m"},
		"3600 seconds (1h0m)": {d: 3600 * time.Second, want: "1h0m"},
		"3900 seconds (1h5m)": {d: 3900 * time.Second, want: "1h5m"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := FormatDuration(tt.d)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatTokensAndCostPrefixesEstimatedUsage(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()

	actual := ansi.Strip(formatTokensAndCost(&sty, 120, 1000, 0, true))

	require.Contains(t, actual, "~12%")
	require.Contains(t, actual, "(120)")
	require.Contains(t, actual, "$0.00")
}

func TestFormatTokensAndCostOmitsEstimatedPrefix(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()

	actual := ansi.Strip(formatTokensAndCost(&sty, 120, 1000, 0, false))

	require.Contains(t, actual, "12%")
	require.NotContains(t, actual, "~12%")
}

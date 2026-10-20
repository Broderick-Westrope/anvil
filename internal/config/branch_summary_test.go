package config_test

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOptionsBranchSummaryPrompt(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		opts *config.Options
		want config.BranchSummaryPrompt
	}{
		"nil options":   {opts: nil, want: config.BranchSummaryAsk},
		"unset":         {opts: &config.Options{}, want: config.BranchSummaryAsk},
		"always":        {opts: &config.Options{BranchSummary: &config.BranchSummaryOptions{Prompt: "always"}}, want: config.BranchSummaryAlways},
		"never":         {opts: &config.Options{BranchSummary: &config.BranchSummaryOptions{Prompt: "never"}}, want: config.BranchSummaryNever},
		"unknown value": {opts: &config.Options{BranchSummary: &config.BranchSummaryOptions{Prompt: "sometimes"}}, want: config.BranchSummaryAsk},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.opts.BranchSummaryPrompt())
		})
	}
}

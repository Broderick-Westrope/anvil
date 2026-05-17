package chat

import (
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestAgentDisplayName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		subagentType string
		description  string
		model        string
		want         string
	}{
		"subagent type capitalised, no model": {
			subagentType: "explorer",
			want:         "Explorer",
		},
		"subagent type with description": {
			subagentType: "explorer",
			description:  "Search auth middleware",
			want:         "Explorer — Search auth middleware",
		},
		"reviewer with opus model override": {
			subagentType: "reviewer",
			model:        "anthropic/claude-opus-4-6",
			want:         "Reviewer (opus-4-6)",
		},
		"subagent type with description and model": {
			subagentType: "fixer",
			description:  "Fix login bug",
			model:        "anthropic/claude-sonnet-4-6",
			want:         "Fixer (sonnet-4-6) — Fix login bug",
		},
		"empty subagent type shows Unknown Agent with description": {
			description: "My task",
			want:        "Unknown Agent — My task",
		},
		"all empty falls back to Unknown Agent": {
			want: "Unknown Agent",
		},
		"model without claude- prefix is kept as-is after slash": {
			subagentType: "explorer",
			model:        "openai/gpt-4o",
			want:         "Explorer (gpt-4o)",
		},
		"model with no slash uses full value": {
			subagentType: "fixer",
			model:        "gpt-4o",
			want:         "Fixer (gpt-4o)",
		},
		"description with model override and no subagent type": {
			description: "Analyse security",
			model:       "anthropic/claude-sonnet-4-6",
			want:        "Unknown Agent (sonnet-4-6) — Analyse security",
		},
		"subagent type with sonnet model": {
			subagentType: "reviewer",
			model:        "anthropic/claude-sonnet-4-6",
			want:         "Reviewer (sonnet-4-6)",
		},
		"hyphenated agent name capitalises each word": {
			subagentType: "devils-advocate",
			want:         "Devils Advocate",
		},
		"hyphenated agent name with description and model": {
			subagentType: "devils-advocate",
			description:  "Review the spec",
			model:        "anthropic/claude-opus-4-6",
			want:         "Devils Advocate (opus-4-6) — Review the spec",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := agentDisplayName(tt.subagentType, tt.description, tt.model)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDrillableAgentStateElapsed(t *testing.T) {
	t.Parallel()

	newState := func() *drillableAgentState {
		return &drillableAgentState{clearFunc: func() {}}
	}

	t.Run("elapsed is empty before start time is set", func(t *testing.T) {
		t.Parallel()

		state := newState()
		require.Empty(t, state.elapsed())
	})

	t.Run("SetStartedAt ignores zero time", func(t *testing.T) {
		t.Parallel()

		state := newState()
		startedAt := time.Unix(100, 0)
		state.SetStartedAt(startedAt)
		state.SetStartedAt(time.Time{})

		require.Equal(t, startedAt, state.startedAt)
	})

	t.Run("SetStartedAt keeps earliest non-zero time", func(t *testing.T) {
		t.Parallel()

		state := newState()
		late := time.Unix(200, 0)
		early := time.Unix(100, 0)
		state.SetStartedAt(late)
		state.SetStartedAt(early)
		state.SetStartedAt(time.Unix(300, 0))

		require.Equal(t, early, state.startedAt)
	})

	t.Run("SetFinishedAt ignores zero time", func(t *testing.T) {
		t.Parallel()

		state := newState()
		finishedAt := time.Unix(200, 0)
		state.SetFinishedAt(finishedAt)
		state.SetFinishedAt(time.Time{})

		require.Equal(t, finishedAt, state.finishedAt)
	})

	t.Run("elapsed uses fixed finished duration", func(t *testing.T) {
		t.Parallel()

		state := newState()
		state.SetStartedAt(time.Unix(100, 0))
		state.SetFinishedAt(time.Unix(145, 0))

		require.Equal(t, "45s", state.elapsed())
	})

	t.Run("elapsed clamps negative finished duration", func(t *testing.T) {
		t.Parallel()

		state := newState()
		state.SetStartedAt(time.Unix(200, 0))
		state.SetFinishedAt(time.Unix(100, 0))

		require.Equal(t, "0s", state.elapsed())
	})

	t.Run("elapsed uses live duration while unfinished", func(t *testing.T) {
		t.Parallel()

		state := newState()
		state.SetStartedAt(time.Now().Add(-2 * time.Second))

		require.NotEmpty(t, state.elapsed())
	})
}

func TestAgentBreadcrumbLabel(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		subagentType string
		description  string
		want         string
	}{
		"type and description": {
			subagentType: "explorer",
			description:  "Search auth",
			want:         "Explorer: Search auth",
		},
		"hyphenated type, no description": {
			subagentType: "devils-advocate",
			want:         "Devils Advocate",
		},
		"no type, with description": {
			description: "Search auth",
			want:        "Agent: Search auth",
		},
		"no type, no description": {
			want: "Agent",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := agentBreadcrumbLabel(tt.subagentType, tt.description)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatAgentTokens(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		tokens      int64
		abbreviated bool
		want        string
	}{
		// Zero always returns empty regardless of mode.
		"zero abbreviated":     {tokens: 0, abbreviated: true, want: ""},
		"zero not abbreviated": {tokens: 0, abbreviated: false, want: ""},

		// Abbreviated mode: bare number only.
		"500 abbreviated":     {tokens: 500, abbreviated: true, want: "500"},
		"1000 abbreviated":    {tokens: 1000, abbreviated: true, want: "1.0k"},
		"1500 abbreviated":    {tokens: 1500, abbreviated: true, want: "1.5k"},
		"1000000 abbreviated": {tokens: 1_000_000, abbreviated: true, want: "1.0M"},
		"1500000 abbreviated": {tokens: 1_500_000, abbreviated: true, want: "1.5M"},

		// Non-abbreviated mode: number + " tokens" label.
		"500 full":     {tokens: 500, abbreviated: false, want: "500 tokens"},
		"1000 full":    {tokens: 1000, abbreviated: false, want: "1.0k tokens"},
		"1500 full":    {tokens: 1500, abbreviated: false, want: "1.5k tokens"},
		"1000000 full": {tokens: 1_000_000, abbreviated: false, want: "1.0M tokens"},
		"1500000 full": {tokens: 1_500_000, abbreviated: false, want: "1.5M tokens"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := formatAgentTokens(tt.tokens, tt.abbreviated)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatStatsLine(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()

	t.Run("zero values contain 0 turns and 0 tools", func(t *testing.T) {
		t.Parallel()
		result := formatStatsLine(&sty, 0, 0, 0, 0, "", 100)
		plain := ansi.Strip(result)
		require.Contains(t, plain, "0 turns")
		require.Contains(t, plain, "0 tools")
	})

	t.Run("normal values full format", func(t *testing.T) {
		t.Parallel()
		// 4200 tokens → "4.2k tokens", cost $0.02, elapsed "14s".
		result := formatStatsLine(&sty, 3, 12, 4200, 0.02, "14s", 100)
		plain := ansi.Strip(result)
		require.Contains(t, plain, "3 turns")
		require.Contains(t, plain, "12 tools")
		require.Contains(t, plain, "4.2k tokens")
		require.Contains(t, plain, "$0.02")
		require.Contains(t, plain, "14s")
	})

	t.Run("token 500 shown as bare number", func(t *testing.T) {
		t.Parallel()
		result := formatStatsLine(&sty, 0, 0, 500, 0, "", 100)
		plain := ansi.Strip(result)
		require.Contains(t, plain, "500 tokens")
	})

	t.Run("token 1500 formatted as 1.5k", func(t *testing.T) {
		t.Parallel()
		result := formatStatsLine(&sty, 0, 0, 1500, 0, "", 100)
		plain := ansi.Strip(result)
		require.Contains(t, plain, "1.5k tokens")
	})

	t.Run("token 1500000 formatted as 1.5M", func(t *testing.T) {
		t.Parallel()
		result := formatStatsLine(&sty, 0, 0, 1_500_000, 0, "", 100)
		plain := ansi.Strip(result)
		require.Contains(t, plain, "1.5M tokens")
	})

	t.Run("narrow width uses abbreviated format", func(t *testing.T) {
		t.Parallel()
		// Width <80 triggers narrow/abbreviated mode.
		result := formatStatsLine(&sty, 3, 12, 1500, 0, "", 79)
		plain := ansi.Strip(result)
		require.Contains(t, plain, "3t")
		require.Contains(t, plain, "12tl")
		require.Contains(t, plain, "1.5k")
		require.NotContains(t, plain, "turns")
		require.NotContains(t, plain, "tools")
		require.NotContains(t, plain, "tokens")
	})
}

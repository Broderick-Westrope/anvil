package agent

import (
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/vercel"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

// providerModel is a fantasy.LanguageModel that only reports its provider
// and model names.
type providerModel struct {
	fantasy.LanguageModel
	provider, model string
}

func (m providerModel) Provider() string { return m.provider }
func (m providerModel) Model() string    { return m.model }

func TestNewRowNormalisesAndCopiesFacts(t *testing.T) {
	t.Parallel()

	a := &sessionAgent{depth: 2, workingDir: "/work"}
	match := true
	c := &stepCapture{
		runID:     "run",
		kind:      usageKindTurn,
		agent:     "coder",
		sessionID: "sess",
		parentID:  "parent",
		messageID: "msg",
		stepIndex: 3,
		attempt:   1,
		model: Model{
			Model: providerModel{provider: google.Name, model: "gemini"},
			CatwalkCfg: catwalk.Model{
				CostPer1MIn:        1,
				CostPer1MOut:       2,
				CostPer1MInCached:  3,
				CostPer1MOutCached: 4,
			},
			ModelCfg: config.SelectedModel{Provider: "my-google", Model: "gemini-pro"},
			FlatRate: true,
		},
		started: time.UnixMilli(1000),
		retries: 2,
		prefix:  &match,
	}
	c.fp.ToolsHash = "tools"
	c.fp.SystemHash = "system"
	c.fp.HistoryHash = "history"
	c.fp.ToolCount = 4
	c.fp.SystemCount = 1
	c.fp.MessageCount = 5
	c.fp.Err = "boom"

	usage := fantasy.Usage{InputTokens: 100, CacheReadTokens: 60, OutputTokens: 7, ReasoningTokens: 3}
	row := a.newRow(c, usage, fantasy.FinishReasonStop, nil, time.UnixMilli(2500))

	// Google includes cache reads in its input count.
	require.Equal(t, int64(40), row.InputTokens)
	require.Equal(t, int64(60), row.CacheReadTokens)
	require.Equal(t, int64(7), row.OutputTokens)
	require.Equal(t, int64(3), row.ReasoningTokens)
	require.Zero(t, row.Estimated)
	require.Contains(t, row.RawUsage, `"input_tokens":100`)

	require.Equal(t, 1.0, row.PriceInput)
	require.Equal(t, 2.0, row.PriceOutput)
	require.Equal(t, 4.0, row.PriceCacheRead)
	require.Equal(t, 3.0, row.PriceCacheWrite)
	require.Equal(t, int64(1), row.FlatRate)

	require.Equal(t, "sess", row.SessionID)
	require.Equal(t, "parent", row.ParentSessionID)
	require.Equal(t, "/work", row.WorkingDir)
	require.Equal(t, "msg", row.MessageID)
	require.Equal(t, "coder", row.Agent)
	require.Equal(t, usageKindTurn, row.Kind)
	require.Equal(t, int64(2), row.Depth)
	require.Equal(t, "run", row.RunID)
	require.Equal(t, int64(3), row.StepIndex)
	require.Equal(t, int64(1), row.Attempt)
	require.Equal(t, "my-google", row.Provider)
	require.Equal(t, google.Name, row.ProviderType)
	require.Equal(t, "gemini-pro", row.Model)
	require.Equal(t, int64(1000), row.RequestStartedAt)
	require.Equal(t, int64(2500), row.ResponseFinishedAt)
	require.Equal(t, int64(2), row.RetryCount)
	require.Equal(t, string(fantasy.FinishReasonStop), row.FinishReason)
	require.Equal(t, cachePolicyAutomatic, row.CachePolicy)

	require.Equal(t, "tools", row.ToolsHash)
	require.Equal(t, "system", row.SystemHash)
	require.Equal(t, "history", row.HistoryHash)
	require.Equal(t, int64(4), row.ToolCount)
	require.Equal(t, int64(1), row.SystemCount)
	require.Equal(t, int64(5), row.MessageCount)
	require.Equal(t, "boom", row.FingerprintError)
	require.True(t, row.HistoryPrefixMatch.Valid)
	require.Equal(t, int64(1), row.HistoryPrefixMatch.Int64)
}

func TestNewRowPrefixMismatchAndUnknown(t *testing.T) {
	t.Parallel()

	a := &sessionAgent{}
	model := Model{Model: providerModel{provider: anthropic.Name, model: "claude"}}
	usage := fantasy.Usage{InputTokens: 1}

	unknown := a.newRow(&stepCapture{model: model}, usage, fantasy.FinishReasonStop, nil, time.Now())
	require.False(t, unknown.HistoryPrefixMatch.Valid)

	mismatch := false
	row := a.newRow(&stepCapture{model: model, prefix: &mismatch}, usage, fantasy.FinishReasonStop, nil, time.Now())
	require.True(t, row.HistoryPrefixMatch.Valid)
	require.Zero(t, row.HistoryPrefixMatch.Int64)

	// Without a config selection the fantasy names are used.
	require.Equal(t, anthropic.Name, row.Provider)
	require.Equal(t, "claude", row.Model)
}

func TestNewRowEstimatesZeroUsage(t *testing.T) {
	t.Parallel()

	a := &sessionAgent{}
	messages := []fantasy.Message{
		fantasy.NewSystemMessage("system prompt"),
		fantasy.NewUserMessage("a user message that is long enough to count"),
	}
	c := &stepCapture{
		model:    Model{Model: providerModel{provider: "kronk", model: "local"}},
		messages: messages,
	}
	row := a.newRow(c, fantasy.Usage{}, fantasy.FinishReasonStop, nil, time.Now())

	require.Equal(t, int64(1), row.Estimated)
	require.Equal(t, estimateMessageTokens(messages), row.InputTokens)
	require.Positive(t, row.InputTokens)
	require.Zero(t, row.CacheReadTokens)
	require.Contains(t, row.RawUsage, `"input_tokens":0`)
	require.Equal(t, cachePolicyNone, row.CachePolicy)
}

func TestCachePolicy(t *testing.T) {
	cases := map[string]string{
		anthropic.Name:    cachePolicyAnthropicEphemeral,
		bedrock.Name:      cachePolicyAnthropicEphemeral,
		vercel.Name:       cachePolicyAnthropicEphemeral,
		openai.Name:       cachePolicyAutomatic,
		"azure":           cachePolicyAutomatic,
		openaicompat.Name: cachePolicyAutomatic,
		"openrouter":      cachePolicyAutomatic,
		google.Name:       cachePolicyAutomatic,
		"kronk":           cachePolicyNone,
		"":                cachePolicyNone,
	}
	for provider, want := range cases {
		require.Equal(t, want, cachePolicy(provider), provider)
	}

	t.Run("env override", func(t *testing.T) {
		t.Setenv("ANVIL_DISABLE_ANTHROPIC_CACHE", "true")
		require.Equal(t, cachePolicyDisabled, cachePolicy(anthropic.Name))
		require.Equal(t, cachePolicyDisabled, cachePolicy(bedrock.Name))
		require.Equal(t, cachePolicyDisabled, cachePolicy(vercel.Name))
		// The override only removes Anthropic-style markers; automatic
		// caching is unaffected.
		require.Equal(t, cachePolicyAutomatic, cachePolicy(openai.Name))
		require.Equal(t, cachePolicyNone, cachePolicy("kronk"))
	})
}

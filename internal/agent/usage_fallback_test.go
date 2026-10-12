package agent

import (
	"errors"
	"fmt"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openrouter"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

func TestUsageIsZero(t *testing.T) {
	t.Parallel()

	require.True(t, usageIsZero(fantasy.Usage{}))
	require.False(t, usageIsZero(fantasy.Usage{InputTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{OutputTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{TotalTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{ReasoningTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{CacheCreationTokens: 1}))
	require.False(t, usageIsZero(fantasy.Usage{CacheReadTokens: 1}))
}

func TestFallbackStepUsageKeepsProviderUsage(t *testing.T) {
	t.Parallel()

	usage := fantasy.Usage{
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
	}
	step := fantasy.StepResult{
		Response: fantasy.Response{Usage: usage},
	}

	fallbackUsage, estimated := fallbackStepUsage(nil, step)
	require.False(t, estimated)
	require.Equal(t, usage, fallbackUsage)
}

func TestFallbackStepUsageEstimatesPromptAndAssistantText(t *testing.T) {
	t.Parallel()

	messages := []fantasy.Message{
		fantasy.NewUserMessage("please explain the implementation details"),
	}
	step := fantasy.StepResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.TextContent{Text: "the implementation stores state safely"},
			},
		},
	}

	usage, estimated := fallbackStepUsage(messages, step)
	require.True(t, estimated)
	require.Positive(t, usage.InputTokens)
	require.Positive(t, usage.OutputTokens)
	require.Equal(t, usage.InputTokens+usage.OutputTokens, usage.TotalTokens)
}

func TestFallbackStepUsageEstimatesReasoning(t *testing.T) {
	t.Parallel()

	messages := []fantasy.Message{
		{
			Role: fantasy.MessageRoleAssistant,
			Content: []fantasy.MessagePart{
				fantasy.ReasoningPart{Text: "first reason about the request"},
			},
		},
	}
	step := fantasy.StepResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.ReasoningContent{Text: "second reason about the answer"},
			},
		},
	}

	usage, estimated := fallbackStepUsage(messages, step)
	require.True(t, estimated)
	require.Positive(t, usage.InputTokens)
	require.Positive(t, usage.OutputTokens)
}

func TestFallbackStepUsageEstimatesToolCalls(t *testing.T) {
	t.Parallel()

	step := fantasy.StepResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.ToolCallContent{
					ToolCallID: "tool-call-1",
					ToolName:   "view",
					Input:      `{"file_path":"/tmp/example.go"}`,
				},
			},
		},
	}

	usage, estimated := fallbackStepUsage(nil, step)
	require.True(t, estimated)
	require.Zero(t, usage.InputTokens)
	require.Positive(t, usage.OutputTokens)
	require.Equal(t, usage.OutputTokens, usage.TotalTokens)
}

func TestFallbackStepUsageEstimatesToolResults(t *testing.T) {
	t.Parallel()

	messages := []fantasy.Message{
		{
			Role: fantasy.MessageRoleTool,
			Content: []fantasy.MessagePart{
				fantasy.ToolResultPart{
					ToolCallID: "tool-call-1",
					Output: fantasy.ToolResultOutputContentText{
						Text: "file contents returned by the tool",
					},
				},
				fantasy.ToolResultPart{
					ToolCallID: "tool-call-2",
					Output: fantasy.ToolResultOutputContentError{
						Error: errors.New("permission denied"),
					},
				},
				fantasy.ToolResultPart{
					ToolCallID: "tool-call-3",
					Output: fantasy.ToolResultOutputContentMedia{
						MediaType: "image/png",
						Text:      "screenshot",
						Data:      "abc123",
					},
				},
			},
		},
	}

	usage, estimated := fallbackStepUsage(messages, fantasy.StepResult{})
	require.True(t, estimated)
	require.Positive(t, usage.InputTokens)
	require.Zero(t, usage.OutputTokens)
	require.Equal(t, usage.InputTokens, usage.TotalTokens)
}

func TestFallbackStepUsageSkipsClientToolResultsAsOutput(t *testing.T) {
	t.Parallel()

	step := fantasy.StepResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.ToolResultContent{
					ToolCallID: "tool-call-1",
					ToolName:   "bash",
					Result: fantasy.ToolResultOutputContentText{
						Text: "large client-executed payload that should not count as model output tokens",
					},
				},
			},
		},
	}

	usage, estimated := fallbackStepUsage(nil, step)
	require.False(t, estimated)
	require.Zero(t, usage.OutputTokens)
}

func TestFallbackStepUsageCountsProviderToolResultsAsOutput(t *testing.T) {
	t.Parallel()

	step := fantasy.StepResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.ToolResultContent{
					ToolCallID:       "tool-call-1",
					ToolName:         "web_search",
					ProviderExecuted: true,
					ClientMetadata:   "provider metadata",
					Result:           fantasy.ToolResultOutputContentText{Text: "provider-executed result"},
				},
			},
		},
	}

	usage, estimated := fallbackStepUsage(nil, step)
	require.True(t, estimated)
	require.Positive(t, usage.OutputTokens)
	require.Equal(t, usage.OutputTokens, usage.TotalTokens)
}

func TestFallbackStepUsageReturnsZeroWithoutContent(t *testing.T) {
	t.Parallel()

	usage, estimated := fallbackStepUsage(nil, fantasy.StepResult{})
	require.False(t, estimated)
	require.True(t, usageIsZero(usage))
}

func TestUpdateSessionUsageSkipsEstimatedCost(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{ID: "session-id", Cost: 1.25}
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{InputTokens: 1000, OutputTokens: 2000}

	agent.updateSessionUsage(model, currentSession, usage, nil, true)

	require.Equal(t, 1.25, currentSession.Cost)
	require.Equal(t, int64(1000), currentSession.PromptTokens)
	require.Equal(t, int64(2000), currentSession.CompletionTokens)
	require.True(t, currentSession.EstimatedUsage)
}

func TestUpdateSessionUsageKeepsCountersForZeroUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
		Cost:             1.25,
	}
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}

	agent.updateSessionUsage(model, currentSession, fantasy.Usage{}, nil, false)

	require.Equal(t, 1.25, currentSession.Cost)
	require.Equal(t, int64(123), currentSession.PromptTokens)
	require.Equal(t, int64(456), currentSession.CompletionTokens)
}

func TestUpdateSessionUsagePreservesOmittedCountersForPartialUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
	}
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{InputTokens: 789}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, int64(789), currentSession.PromptTokens)
	require.Equal(t, int64(456), currentSession.CompletionTokens)
}

func TestUpdateSessionUsagePreservesCountersForTotalOnlyUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
	}
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{TotalTokens: 100}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, int64(123), currentSession.PromptTokens)
	require.Equal(t, int64(456), currentSession.CompletionTokens)
}

func TestUpdateSessionUsagePreservesPromptForOutputOnlyUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
	}
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{OutputTokens: 50}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, int64(123), currentSession.PromptTokens)
	require.Equal(t, int64(50), currentSession.CompletionTokens)
}

func TestUpdateSessionUsageKeepsCountersForEstimatedZeroUsage(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{
		ID:               "session-id",
		PromptTokens:     123,
		CompletionTokens: 456,
		Cost:             1.25,
	}
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}

	agent.updateSessionUsage(model, currentSession, fantasy.Usage{}, nil, true)

	require.Equal(t, 1.25, currentSession.Cost)
	require.Equal(t, int64(123), currentSession.PromptTokens)
	require.Equal(t, int64(456), currentSession.CompletionTokens)
}

func TestSummaryCompletionTokens(t *testing.T) {
	t.Parallel()

	summaryMessage := message.Message{
		Parts: []message.ContentPart{
			message.TextContent{Text: "summary text"},
			message.ReasoningContent{Thinking: "reasoning text"},
		},
	}

	require.Equal(t, int64(42), summaryCompletionTokens(fantasy.Usage{OutputTokens: 42}, summaryMessage))
	require.Equal(t, approxTokenCount("summary text")+approxTokenCount("reasoning text"), summaryCompletionTokens(fantasy.Usage{}, summaryMessage))
	require.Zero(t, summaryCompletionTokens(fantasy.Usage{}, message.Message{}))
}

func TestUpdateSessionUsageAddsProviderCost(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{}
	currentSession := &session.Session{ID: "session-id", Cost: 1.25}
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 10, CostPer1MOut: 20}}
	usage := fantasy.Usage{InputTokens: 1000, OutputTokens: 2000}

	agent.updateSessionUsage(model, currentSession, usage, nil, false)

	require.Equal(t, 1.3, currentSession.Cost)
	require.Equal(t, int64(1000), currentSession.PromptTokens)
	require.Equal(t, int64(2000), currentSession.CompletionTokens)
	require.False(t, currentSession.EstimatedUsage)
}

func TestSummarizeChargesSessionCost(t *testing.T) {
	t.Parallel()
	usage := fantasy.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	tests := map[string]struct {
		// openrouterCosts holds the cost OpenRouter reports for each step,
		// nil for none. Every step but the last calls a tool the summary
		// doesn't have, so the model is asked again.
		openrouterCosts []*float64
		flatRate        bool
		want            float64
	}{
		"catwalk prices":             {openrouterCosts: []*float64{nil}, want: 3},
		"openrouter cost":            {openrouterCosts: []*float64{new(0.25)}, want: 0.25},
		"openrouter cost over steps": {openrouterCosts: []*float64{new(0.25), nil, new(0.5)}, want: 0.75},
		"flat rate":                  {openrouterCosts: []*float64{nil}, flatRate: true},
		"flat rate override":         {openrouterCosts: []*float64{new(0.25)}, flatRate: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			model := &recordingModel{respond: func(call int) ([]fantasy.StreamPart, error) {
				parts := textReply("Summary.", fantasy.FinishReasonStop, usage)
				if call < len(tt.openrouterCosts)-1 {
					parts = toolCallReply(fmt.Sprintf("call-%d", call), "missing", usage)
				}
				if cost := tt.openrouterCosts[call]; cost != nil {
					parts[len(parts)-1].ProviderMetadata = fantasy.ProviderMetadata{
						openrouter.Name: &openrouter.ProviderMetadata{Usage: openrouter.UsageAccounting{Cost: *cost}},
					}
				}
				return parts, nil
			}}
			m := Model{
				Model:      model,
				CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000, CostPer1MIn: 1, CostPer1MOut: 2},
				FlatRate:   tt.flatRate,
			}
			a := sideCallAgent(env, m, m, "", config.ProviderConfig{})
			sessionID, _ := sessionWithUserMessage(t, env)

			require.NoError(t, a.Summarize(t.Context(), sessionID, nil))

			require.Len(t, model.recorded(), len(tt.openrouterCosts))
			sess, err := env.sessions.Get(t.Context(), sessionID)
			require.NoError(t, err)
			require.InDelta(t, tt.want, sess.Cost, 1e-9)
		})
	}
}

func TestUpdateSessionUsageCost(t *testing.T) {
	t.Parallel()
	usage := fantasy.Usage{InputTokens: 1_000_000, CacheCreationTokens: 2_000_000, CacheReadTokens: 3_000_000, OutputTokens: 4_000_000}
	model := Model{CatwalkCfg: catwalk.Model{CostPer1MIn: 1, CostPer1MInCached: 10, CostPer1MOutCached: 100, CostPer1MOut: 1000}}
	tests := map[string]struct {
		override  *float64
		estimated bool
		want      float64
	}{
		"catwalk rates":      {want: 1*1 + 2*10 + 3*100 + 4*1000},
		"override":           {override: new(0.5), want: 0.5},
		"estimated override": {override: new(0.5), estimated: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			currentSession := &session.Session{ID: "session-id", Cost: 1.25}

			(&sessionAgent{}).updateSessionUsage(model, currentSession, usage, tt.override, tt.estimated)

			require.InDelta(t, 1.25+tt.want, currentSession.Cost, 1e-9)
		})
	}
}

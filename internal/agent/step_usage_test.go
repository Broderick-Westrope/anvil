package agent

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"sync"
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
	"github.com/Broderick-Westrope/anvil/internal/message"
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

// usageRow is the subset of a step_usage_report row the tests check.
type usageRow struct {
	kind, runID, agent, sessionID, messageID string
	provider, model, finishReason            string
	toolsHash, systemHash, cachePolicy       string
	stepIndex, attempt, retryCount           int64
	input, cacheRead, cacheWrite, estimated  int64
	prefixMatch                              sql.NullInt64
}

// stepUsageRows flushes env's recorder and returns step_usage_report rows
// in the order they were recorded.
func stepUsageRows(t *testing.T, env fakeEnv) []usageRow {
	t.Helper()
	require.NoError(t, env.usage.Close(t.Context()))
	rows, err := env.conn.QueryContext(t.Context(), `SELECT r.kind, r.run_id, r.agent, r.session_id,
		r.message_id, r.provider, r.model, r.finish_reason, r.tools_hash, r.system_hash,
		r.cache_policy, r.step_index, r.attempt, r.retry_count, r.input_tokens,
		r.cache_read_tokens, r.cache_write_tokens, r.estimated, r.history_prefix_match
		FROM step_usage_report r JOIN step_usage s ON s.id = r.id ORDER BY s.rowid`)
	require.NoError(t, err)
	defer rows.Close()
	var out []usageRow
	for rows.Next() {
		var r usageRow
		require.NoError(t, rows.Scan(&r.kind, &r.runID, &r.agent, &r.sessionID, &r.messageID,
			&r.provider, &r.model, &r.finishReason, &r.toolsHash, &r.systemHash, &r.cachePolicy,
			&r.stepIndex, &r.attempt, &r.retryCount, &r.input, &r.cacheRead, &r.cacheWrite,
			&r.estimated, &r.prefixMatch))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func filterUsageRows(rows []usageRow, kind string) []usageRow {
	var out []usageRow
	for _, r := range rows {
		if r.kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// usageModel is a fantasy.LanguageModel whose replies are scripted per
// call by respond, which receives the 0-based call number.
type usageModel struct {
	fantasy.LanguageModel
	name    string
	mu      sync.Mutex
	calls   int
	respond func(call int) ([]fantasy.StreamPart, error)
}

func (m *usageModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	call := m.calls
	m.calls++
	m.mu.Unlock()
	parts, err := m.respond(call)
	if err != nil {
		return nil, err
	}
	return slices.Values(parts), nil
}

func (m *usageModel) Provider() string { return m.name }
func (m *usageModel) Model() string    { return m.name + "-model" }

func textReply(text string, reason fantasy.FinishReason, usage fantasy.Usage) []fantasy.StreamPart {
	return []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeTextStart, ID: "text"},
		{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: text},
		{Type: fantasy.StreamPartTypeTextEnd, ID: "text"},
		{Type: fantasy.StreamPartTypeFinish, FinishReason: reason, Usage: usage},
	}
}

func toolCallReply(id, toolName string, usage fantasy.Usage) []fantasy.StreamPart {
	return []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeToolInputStart, ID: id, ToolCallName: toolName},
		{Type: fantasy.StreamPartTypeToolInputEnd, ID: id},
		{Type: fantasy.StreamPartTypeToolCall, ID: id, ToolCallName: toolName, ToolCallInput: "{}"},
		{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls, Usage: usage},
	}
}

func TestStepUsageRecordedWhenToolFailsStep(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Tool error", t.TempDir())
	require.NoError(t, err)

	failing := fantasy.NewAgentTool("explode", "Fails the step.",
		func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.ToolResponse{}, errors.New("tool exploded")
		},
	)
	model := &usageModel{name: "scripted", respond: func(int) ([]fantasy.StreamPart, error) {
		return toolCallReply("call_0", "explode", fantasy.Usage{InputTokens: 10, CacheReadTokens: 90, OutputTokens: 5}), nil
	}}
	a := testSessionAgent(env, model, model, "system", failing)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.ErrorContains(t, err, "tool exploded")

	rows := stepUsageRows(t, env)
	require.Len(t, rows, 1)
	require.Equal(t, usageKindTurn, rows[0].kind)
	require.Equal(t, string(fantasy.FinishReasonToolCalls), rows[0].finishReason)
	require.Equal(t, int64(10), rows[0].input)
	require.Equal(t, int64(90), rows[0].cacheRead)
	require.Equal(t, sess.ID, rows[0].sessionID)
	require.Equal(t, "orchestrator", rows[0].agent)
}

func TestStepUsageRecordsRetriedRequestOnce(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Retry", t.TempDir())
	require.NoError(t, err)

	model := &usageModel{name: "scripted", respond: func(call int) ([]fantasy.StreamPart, error) {
		if call == 0 {
			// retry-after-ms keeps fantasy's backoff short.
			return nil, &fantasy.ProviderError{
				Title:           "overloaded",
				Message:         "try again",
				StatusCode:      http.StatusServiceUnavailable,
				ResponseHeaders: map[string]string{"retry-after-ms": "1"},
			}
		}
		return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 3, OutputTokens: 1}), nil
	}}
	a := testSessionAgent(env, model, model, "system")

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)

	rows := stepUsageRows(t, env)
	require.Len(t, rows, 1)
	require.Equal(t, int64(1), rows[0].retryCount)
	require.Equal(t, int64(3), rows[0].input)
}

func TestStepUsageRecordsEveryTitleAttempt(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Title", t.TempDir())
	require.NoError(t, err)

	small := &usageModel{name: "small", respond: func(int) ([]fantasy.StreamPart, error) {
		return textReply("A title that never", fantasy.FinishReasonLength, fantasy.Usage{InputTokens: 20, OutputTokens: 40}), nil
	}}
	large := &usageModel{name: "large", respond: func(int) ([]fantasy.StreamPart, error) {
		return textReply("Short title", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 20, OutputTokens: 3}), nil
	}}
	a := testSessionAgent(env, large, small, "system").(*sessionAgent)

	a.generateTitle(t.Context(), sess.ID, []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "Help me name this"}},
	}})

	rows := stepUsageRows(t, env)
	require.Len(t, rows, 2)
	for i, row := range rows {
		require.Equal(t, usageKindTitle, row.kind)
		require.Equal(t, int64(i), row.attempt)
		require.Equal(t, rows[0].runID, row.runID)
		require.Equal(t, sess.ID, row.sessionID)
	}
	require.Equal(t, "small", rows[0].provider)
	require.Equal(t, string(fantasy.FinishReasonLength), rows[0].finishReason)
	require.Equal(t, "large", rows[1].provider)
	require.Equal(t, string(fantasy.FinishReasonStop), rows[1].finishReason)

	renamed, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, "Short title", renamed.Title)
}

func TestStepUsageDetectsRewrittenHistory(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Prefix", t.TempDir())
	require.NoError(t, err)

	model := &usageModel{name: "scripted", respond: func(int) ([]fantasy.StreamPart, error) {
		return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 3, OutputTokens: 1}), nil
	}}
	a := testSessionAgent(env, model, model, "system")
	run := func(prompt string) {
		t.Helper()
		_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: prompt, NonInteractive: true})
		require.NoError(t, err)
	}

	run("first")
	run("second")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	idx := slices.IndexFunc(msgs, func(m message.Message) bool {
		return m.Role == message.User && m.Content().Text == "first"
	})
	require.GreaterOrEqual(t, idx, 0)
	first := msgs[idx]
	first.Parts = []message.ContentPart{message.TextContent{Text: "first, rewritten"}}
	require.NoError(t, env.messages.Update(t.Context(), first))
	require.NoError(t, env.messages.FlushAll(t.Context()))

	run("third")

	rows := stepUsageRows(t, env)
	require.Len(t, rows, 3)
	// No earlier turn in this process.
	require.False(t, rows[0].prefixMatch.Valid)
	// The second run extends the first run's history.
	require.True(t, rows[1].prefixMatch.Valid)
	require.Equal(t, int64(1), rows[1].prefixMatch.Int64)
	// The third run's history no longer starts with what was sent before.
	require.True(t, rows[2].prefixMatch.Valid)
	require.Zero(t, rows[2].prefixMatch.Int64)
	require.NotEqual(t, rows[0].runID, rows[1].runID)
}

func TestStepUsageNilRecorder(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Nil recorder", t.TempDir())
	require.NoError(t, err)

	model := &usageModel{name: "scripted", respond: func(int) ([]fantasy.StreamPart, error) {
		return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 3, OutputTokens: 1}), nil
	}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{Model: model},
		SmallModel: Model{Model: model},
		Sessions:   env.sessions,
		Messages:   env.messages,
	})
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)
	require.Empty(t, stepUsageRows(t, env))
}

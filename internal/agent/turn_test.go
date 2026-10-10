package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/Broderick-Westrope/anvil/internal/agent/notify"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// recordingModel is a fantasy.LanguageModel that replies with the stream
// parts respond returns for each 0-based call, and records every call.
type recordingModel struct {
	fantasy.LanguageModel
	respond func(call int) ([]fantasy.StreamPart, error)
	mu      sync.Mutex
	calls   []fantasy.Call
}

func (m *recordingModel) Stream(_ context.Context, c fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	n := len(m.calls)
	c.Prompt = cloneFantasyMessages(c.Prompt)
	m.calls = append(m.calls, c)
	m.mu.Unlock()
	parts, err := m.respond(n)
	if err != nil {
		return nil, err
	}
	return slices.Values(parts), nil
}

func (m *recordingModel) Provider() string { return "recording" }
func (m *recordingModel) Model() string    { return "recording-model" }

func (m *recordingModel) recorded() []fantasy.Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// turnAgent builds a session agent around model with a context window of
// contextWindow tokens.
func turnAgent(env fakeEnv, model fantasy.LanguageModel, systemPrompt string, contextWindow int64, agentTools ...fantasy.AgentTool) *sessionAgent {
	m := Model{
		Model:      model,
		CatwalkCfg: catwalk.Model{ContextWindow: contextWindow, DefaultMaxTokens: 1000},
	}
	return NewSessionAgent(SessionAgentOptions{
		LargeModel:   m,
		SmallModel:   m,
		SystemPrompt: systemPrompt,
		IsYolo:       true,
		Sessions:     env.sessions,
		Messages:     env.messages,
		Tools:        agentTools,
	}).(*sessionAgent)
}

func okTool(name string) fantasy.AgentTool {
	return stepTool(name, func(context.Context, int) fantasy.ToolResponse {
		return fantasy.NewTextResponse("ok")
	})
}

// toolInputThenError starts a tool call and then fails the stream before
// the call's input is complete.
func toolInputThenError(id, toolName string, err error) []fantasy.StreamPart {
	return []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeToolInputStart, ID: id, ToolCallName: toolName},
		{Type: fantasy.StreamPartTypeError, Error: err},
	}
}

func newTestSession(t *testing.T, env fakeEnv) session.Session {
	t.Helper()
	sess, err := env.sessions.Create(t.Context(), "Turn", t.TempDir())
	require.NoError(t, err)
	return sess
}

func listMessages(t *testing.T, env fakeEnv, sessionID string, role message.MessageRole) []message.Message {
	t.Helper()
	msgs, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	return slices.DeleteFunc(msgs, func(m message.Message) bool { return m.Role != role })
}

// toolResultContents maps each tool call ID to the contents of its
// persisted results, in order.
func toolResultContents(t *testing.T, env fakeEnv, sessionID string) map[string][]string {
	t.Helper()
	got := make(map[string][]string)
	for _, msg := range listMessages(t, env, sessionID, message.Tool) {
		for _, tr := range msg.ToolResults() {
			got[tr.ToolCallID] = append(got[tr.ToolCallID], tr.Content)
		}
	}
	return got
}

func TestFailureFinish(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err        error
		wantReason message.FinishReason
		wantTitle  string
		wantDetail string
	}{
		"canceled": {
			err:        context.Canceled,
			wantReason: message.FinishReasonCanceled,
			wantTitle:  "User canceled request",
		},
		"wrapped cancellation": {
			err:        errors.Join(errors.New("stream"), context.Canceled),
			wantReason: message.FinishReasonCanceled,
			wantTitle:  "User canceled request",
		},
		"provider error": {
			err:        &fantasy.ProviderError{Title: "overloaded", Message: "try again later"},
			wantReason: message.FinishReasonError,
			wantTitle:  "Overloaded",
			wantDetail: "try again later",
		},
		"provider error without title": {
			err:        &fantasy.ProviderError{Message: "no title"},
			wantReason: message.FinishReasonError,
			wantTitle:  "Provider Error",
			wantDetail: "no title",
		},
		"fantasy error": {
			err:        &fantasy.Error{Title: "invalid argument", Message: "prompt can't be empty"},
			wantReason: message.FinishReasonError,
			wantTitle:  "Invalid Argument",
			wantDetail: "prompt can't be empty",
		},
		"fantasy error without title": {
			err:        &fantasy.Error{Message: "bare"},
			wantReason: message.FinishReasonError,
			wantTitle:  "Provider Error",
			wantDetail: "bare",
		},
		"transport error": {
			err:        errors.New("read: stream error: stream ID 3; INTERNAL_ERROR"),
			wantReason: message.FinishReasonError,
			wantTitle:  "Stream Transport Error",
			wantDetail: "INTERNAL_ERROR",
		},
		"other error": {
			err:        errors.New("boom"),
			wantReason: message.FinishReasonError,
			wantTitle:  "Provider Error",
			wantDetail: "boom",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reason, title, detail := failureFinish(tc.err)
			require.Equal(t, tc.wantReason, reason)
			require.Equal(t, tc.wantTitle, title)
			require.Equal(t, tc.wantDetail, detail)
		})
	}
}

func TestStopsTurn(t *testing.T) {
	t.Parallel()

	result := func(stops ...bool) fantasy.StepResult {
		var content fantasy.ResponseContent
		for _, stop := range stops {
			content = append(content, fantasy.ToolResultContent{StopTurn: stop})
		}
		return fantasy.StepResult{Response: fantasy.Response{Content: content}}
	}
	tests := map[string]struct {
		step fantasy.StepResult
		want bool
	}{
		"no tool results":        {step: result(), want: false},
		"no result stops":        {step: result(false, false), want: false},
		"a later result stops":   {step: result(false, true), want: true},
		"the first result stops": {step: result(true, false), want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, stopsTurn(tc.step))
		})
	}
}

func TestContextNearlyFull(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		contextWindow int
		promptTokens  int64
		disabled      bool
		want          bool
	}{
		"unknown window":                   {contextWindow: 0, promptTokens: 0, want: false},
		"auto-summarize disabled":          {contextWindow: 1000, promptTokens: 999, disabled: true, want: false},
		"small window with room":           {contextWindow: 1000, promptTokens: 799, want: false},
		"small window at the 20% buffer":   {contextWindow: 1000, promptTokens: 800, want: true},
		"small window past the buffer":     {contextWindow: 1000, promptTokens: 950, want: true},
		"large window with room":           {contextWindow: 200_000, promptTokens: 170_000, want: false},
		"large window at the fixed buffer": {contextWindow: 200_000, promptTokens: 180_000, want: true},
		"large window past the buffer":     {contextWindow: 200_000, promptTokens: 190_000, want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr := &turn{
				a:          &sessionAgent{disableAutoSummarize: tc.disabled},
				largeModel: Model{CatwalkCfg: catwalk.Model{ContextWindow: int64(tc.contextWindow)}},
				session:    session.Session{PromptTokens: tc.promptTokens},
			}
			require.Equal(t, tc.want, tr.contextNearlyFull(nil))
			require.Equal(t, tc.want, tr.shouldSummarize)
		})
	}
}

func TestNotifyWhenFinished(t *testing.T) {
	t.Parallel()

	const sessionID = "session-1"
	tests := map[string]struct {
		owner   any
		wantErr bool
	}{
		"no owner":             {owner: nil, wantErr: true},
		"nil owner":            {owner: (*submissionOwner)(nil), wantErr: true},
		"owner of another":     {owner: &submissionOwner{sessionID: "session-2"}, wantErr: true},
		"owner of the session": {owner: &submissionOwner{sessionID: sessionID}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			broker := pubsub.NewBroker[notify.Notification]()
			t.Cleanup(broker.Shutdown)
			a := NewSessionAgent(SessionAgentOptions{Notify: broker}).(*sessionAgent)
			ctx := t.Context()
			if tc.owner != nil {
				ctx = context.WithValue(ctx, ownerKey{}, tc.owner)
			}

			err := a.notifyWhenFinished(ctx, sessionID, func() string { return "Renamed" })
			if tc.wantErr {
				require.EqualError(t, err, `missing submission owner for session "session-1"`)
				return
			}
			require.NoError(t, err)

			events := broker.Subscribe(t.Context())
			owner := tc.owner.(*submissionOwner)
			owner.onFinish()
			event := <-events
			require.Equal(t, pubsub.CreatedEvent, event.Type)
			require.Equal(t, notify.Notification{
				SessionID:    sessionID,
				SessionTitle: "Renamed",
				Type:         notify.TypeAgentFinished,
			}, event.Payload)
		})
	}
}

func TestRunSendsMaxOutputTokensOnlyWhenSet(t *testing.T) {
	t.Parallel()

	limit := int64(512)
	tests := map[string]struct {
		maxOutputTokens int64
		want            *int64
	}{
		"unset": {maxOutputTokens: 0, want: nil},
		"set":   {maxOutputTokens: 512, want: &limit},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			sess := newTestSession(t, env)
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1}), nil
			}}
			a := turnAgent(env, model, "system", 200_000)

			_, err := a.Run(t.Context(), SessionAgentCall{
				SessionID: sess.ID, Prompt: "go", MaxOutputTokens: tc.maxOutputTokens, NonInteractive: true,
			})
			require.NoError(t, err)

			calls := model.recorded()
			require.Len(t, calls, 1)
			require.Equal(t, tc.want, calls[0].MaxOutputTokens)
		})
	}
}

// Without a system prompt the first message still carries the cache
// marker that would have gone on the system prompt, along with the last
// two messages.
func TestRunMarksFirstMessageForCachingWithoutSystemPrompt(t *testing.T) {
	t.Setenv("ANVIL_DISABLE_ANTHROPIC_CACHE", "")

	env := testEnv(t)
	sess := newTestSession(t, env)
	model := &recordingModel{respond: func(call int) ([]fantasy.StreamPart, error) {
		if call == 0 {
			return toolCallReply("call_0", "work", fantasy.Usage{InputTokens: 1}), nil
		}
		return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1}), nil
	}}
	a := turnAgent(env, model, "", 200_000, okTool("work"))

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)

	calls := model.recorded()
	require.Len(t, calls, 2)
	prompt := calls[1].Prompt
	var roles []fantasy.MessageRole
	var marked []bool
	for _, msg := range prompt {
		roles = append(roles, msg.Role)
		_, ok := msg.ProviderOptions[anthropic.Name]
		marked = append(marked, ok)
	}
	// The todo reminder, the prompt, the tool call and its result.
	require.Equal(t, []fantasy.MessageRole{
		fantasy.MessageRoleUser, fantasy.MessageRoleUser, fantasy.MessageRoleAssistant, fantasy.MessageRoleTool,
	}, roles)
	require.Equal(t, []bool{true, false, true, true}, marked)
}

func TestRunStripsLeadingNewlineOnlyFromFirstText(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess := newTestSession(t, env)
	model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
		return []fantasy.StreamPart{
			{Type: fantasy.StreamPartTypeTextStart, ID: "text"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: "\nhello"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: "\nworld"},
			{Type: fantasy.StreamPartTypeTextEnd, ID: "text"},
			{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
		}, nil
	}}
	a := turnAgent(env, model, "system", 200_000)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)

	assistants := listMessages(t, env, sess.ID, message.Assistant)
	require.Len(t, assistants, 1)
	require.Equal(t, "hello\nworld", assistants[0].Content().Text)
}

func TestRunToolStopTurnEndsTurn(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		stopTurn    bool
		wantReasons []message.FinishReason
	}{
		"tool halts the turn": {
			stopTurn:    true,
			wantReasons: []message.FinishReason{message.FinishReasonEndTurn},
		},
		"tool lets the turn continue": {
			stopTurn:    false,
			wantReasons: []message.FinishReason{message.FinishReasonToolUse, message.FinishReasonEndTurn},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			sess := newTestSession(t, env)
			tool := stepTool("work", func(context.Context, int) fantasy.ToolResponse {
				resp := fantasy.NewTextResponse("ok")
				resp.StopTurn = tc.stopTurn
				return resp
			})
			model := &scriptedModel{toolName: "work", toolSteps: 1}
			a := turnAgent(env, model, "system", 200_000, tool)

			_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
			require.NoError(t, err)

			var reasons []message.FinishReason
			for _, msg := range listMessages(t, env, sess.ID, message.Assistant) {
				reasons = append(reasons, msg.FinishReason())
			}
			require.Equal(t, tc.wantReasons, reasons)
		})
	}
}

func TestJobEvents_FailedJobOutputDoesNotObserveCompletion(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess := newTestSession(t, env)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)

	tool := stepTool(tools.JobOutputToolName, func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 0 {
			store.JobCompleted(completedJob("J01", sess.ID), "")
			return fantasy.WithResponseMetadata(fantasy.NewTextErrorResponse("job_output failed"),
				tools.JobOutputResponseMetadata{ShellID: "J01", Done: true})
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &scriptedModel{toolName: tools.JobOutputToolName, toolSteps: 1}
	require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, model, store, tool), sess.ID))

	require.Len(t, model.prompts, 2)
	got := notices(model.prompts[1])
	require.Len(t, got, 1)
	require.Contains(t, got[0], "- Job J01 completed, exit 1")
}

func TestJobEvents_NoStoreIgnoresJobResults(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess := newTestSession(t, env)
	tool := stepTool(tools.JobOutputToolName, func(context.Context, int) fantasy.ToolResponse {
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse("Status: completed"),
			tools.JobOutputResponseMetadata{ShellID: "J01", Done: true})
	})
	model := &scriptedModel{toolName: tools.JobOutputToolName, toolSteps: 1}
	require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, model, nil, tool), sess.ID))
	require.Len(t, model.prompts, 2)
}

func TestRunFailureClosesOpenToolCall(t *testing.T) {
	t.Parallel()

	streamErr := errors.New("stream broke")
	tests := map[string]struct {
		err        error
		wantReason message.FinishReason
		wantTitle  string
		wantDetail string
		wantResult string
	}{
		"provider failure": {
			err:        streamErr,
			wantReason: message.FinishReasonError,
			wantTitle:  "Provider Error",
			wantDetail: "stream broke",
			wantResult: "There was an error while executing the tool",
		},
		"cancellation": {
			err:        context.Canceled,
			wantReason: message.FinishReasonCanceled,
			wantTitle:  "User canceled request",
			wantResult: "Error: user cancelled assistant tool calling",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			sess := newTestSession(t, env)
			sessionEvents := env.sessions.Subscribe(t.Context())
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				return toolInputThenError("call_0", "work", tc.err), nil
			}}
			a := turnAgent(env, model, "system", 200_000, okTool("work"))

			_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
			require.ErrorIs(t, err, tc.err)

			assistants := listMessages(t, env, sess.ID, message.Assistant)
			require.Len(t, assistants, 1)
			assistant := assistants[0]
			require.Equal(t, []message.ToolCall{{ID: "call_0", Name: "work", Input: "{}", Finished: true}}, assistant.ToolCalls())
			finish := assistant.FinishPart()
			require.NotNil(t, finish)
			require.Equal(t, tc.wantReason, finish.Reason)
			require.Equal(t, tc.wantTitle, finish.Message)
			require.Equal(t, tc.wantDetail, finish.Details)

			toolMsgs := listMessages(t, env, sess.ID, message.Tool)
			require.Len(t, toolMsgs, 1)
			require.Equal(t, assistant.ID, toolMsgs[0].ParentMessageID)
			require.Equal(t, []message.ToolResult{{
				ToolCallID: "call_0", Name: "work", Content: tc.wantResult, IsError: true,
			}}, toolMsgs[0].ToolResults())

			current, err := env.sessions.Get(t.Context(), sess.ID)
			require.NoError(t, err)
			require.Equal(t, toolMsgs[0].ID, current.LeafMessageID)
			require.True(t, publishedLeaf(sessionEvents, sess.ID, toolMsgs[0].ID),
				"the session's new leaf was not published")
		})
	}
}

// publishedLeaf drains the session events already published and reports
// whether one moved sessionID's leaf to leafID.
func publishedLeaf(events <-chan pubsub.Event[session.Session], sessionID, leafID string) bool {
	found := false
	for {
		select {
		case event := <-events:
			if event.Type == pubsub.UpdatedEvent && event.Payload.ID == sessionID && event.Payload.LeafMessageID == leafID {
				found = true
			}
		default:
			return found
		}
	}
}

// A step whose second tool fails critically and whose result can't be
// saved leaves that call unanswered; the failure gives it an error result
// and leaves the first, answered call alone.
func TestRunFailureAnswersOnlyUnansweredToolCalls(t *testing.T) {
	t.Parallel()

	const unanswered = "There was an error while executing the tool"
	tests := map[string]struct {
		rejectWhen  string
		wantErr     string
		wantResults map[string][]string
	}{
		"error result saved": {
			rejectWhen:  `NEW.parts LIKE '%BAD_TOOL_OUTPUT%'`,
			wantErr:     "BAD_TOOL_OUTPUT",
			wantResults: map[string][]string{"call_a": {"fine"}, "call_b": {unanswered}},
		},
		"error result rejected": {
			rejectWhen:  `NEW.parts LIKE '%BAD_TOOL_OUTPUT%' OR NEW.parts LIKE '%` + unanswered + `%'`,
			wantErr:     "insert blocked",
			wantResults: map[string][]string{"call_a": {"fine"}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			sess := newTestSession(t, env)
			_, err := env.conn.ExecContext(t.Context(), "CREATE TRIGGER reject_tool_result BEFORE INSERT ON messages WHEN "+tc.rejectWhen+
				" BEGIN SELECT RAISE(ABORT, 'insert blocked'); END")
			require.NoError(t, err)

			good := okToolWith("good", "fine")
			bad := fantasy.NewAgentTool("bad", "Fails the step.",
				func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
					return fantasy.ToolResponse{}, errors.New("BAD_TOOL_OUTPUT")
				})
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				return []fantasy.StreamPart{
					{Type: fantasy.StreamPartTypeToolInputStart, ID: "call_a", ToolCallName: "good"},
					{Type: fantasy.StreamPartTypeToolInputEnd, ID: "call_a"},
					{Type: fantasy.StreamPartTypeToolCall, ID: "call_a", ToolCallName: "good", ToolCallInput: "{}"},
					{Type: fantasy.StreamPartTypeToolInputStart, ID: "call_b", ToolCallName: "bad"},
					{Type: fantasy.StreamPartTypeToolInputEnd, ID: "call_b"},
					{Type: fantasy.StreamPartTypeToolCall, ID: "call_b", ToolCallName: "bad", ToolCallInput: "{}"},
					{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls},
				}, nil
			}}
			a := turnAgent(env, model, "system", 200_000, good, bad)

			_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
			require.ErrorContains(t, err, tc.wantErr)
			require.Equal(t, tc.wantResults, toolResultContents(t, env, sess.ID))
		})
	}
}

func okToolWith(name, reply string) fantasy.AgentTool {
	return stepTool(name, func(context.Context, int) fantasy.ToolResponse {
		return fantasy.NewTextResponse(reply)
	})
}

// A turn that fills the context window is summarized. The run's summary
// retry hook only runs when summarizing fails.
func TestRunSummaryRetriesOnlyAfterFailure(t *testing.T) {
	t.Parallel()

	toolStep := toolCallReply("call_0", "work", fantasy.Usage{InputTokens: 95, OutputTokens: 1})
	text := func(s string) []fantasy.StreamPart {
		return textReply(s, fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1, OutputTokens: 1})
	}
	summaryErr := errors.New("summary failed")
	tests := map[string]struct {
		replies     []func() ([]fantasy.StreamPart, error)
		retryErr    error
		wantErr     error
		wantRetried []error
	}{
		"summary succeeds": {
			replies: []func() ([]fantasy.StreamPart, error){
				func() ([]fantasy.StreamPart, error) { return toolStep, nil },
				func() ([]fantasy.StreamPart, error) { return text("summary"), nil },
			},
		},
		"summary succeeds after retry": {
			replies: []func() ([]fantasy.StreamPart, error){
				func() ([]fantasy.StreamPart, error) { return toolStep, nil },
				func() ([]fantasy.StreamPart, error) { return nil, summaryErr },
				func() ([]fantasy.StreamPart, error) { return text("summary"), nil },
			},
			wantRetried: []error{summaryErr},
		},
		"retry declined": {
			replies: []func() ([]fantasy.StreamPart, error){
				func() ([]fantasy.StreamPart, error) { return toolStep, nil },
				func() ([]fantasy.StreamPart, error) { return nil, summaryErr },
			},
			retryErr:    errors.New("cannot refresh"),
			wantErr:     summaryErr,
			wantRetried: []error{summaryErr},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			sess := newTestSession(t, env)
			model := &recordingModel{respond: func(call int) ([]fantasy.StreamPart, error) {
				if call < len(tc.replies) {
					return tc.replies[call]()
				}
				return text("continued"), nil
			}}
			a := turnAgent(env, model, "system", 100, okTool("work"))
			var mu sync.Mutex
			var retried []error

			_, err := a.Run(t.Context(), SessionAgentCall{
				SessionID: sess.ID, Prompt: "go", NonInteractive: true,
				retrySummary: func(_ context.Context, err error) error {
					mu.Lock()
					defer mu.Unlock()
					retried = append(retried, err)
					return tc.retryErr
				},
			})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			mu.Lock()
			defer mu.Unlock()
			require.Len(t, retried, len(tc.wantRetried))
			for i, want := range tc.wantRetried {
				require.ErrorIs(t, retried[i], want)
			}
		})
	}
}

// lockedBuffer is a goroutine-safe bytes.Buffer for capturing logs.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// These subtests replace the default logger, so they don't run in
// parallel.
func TestRunLogsOnlyRealProblems(t *testing.T) {
	const (
		contentFilterLog = "Provider content filter stopped the model"
		resetLog         = "Failed to reset message on retry"
		syncLeafLog      = "Failed to sync session leaf after error"
		flushLog         = "Failed to flush pending message updates after run"
	)
	tests := map[string]struct {
		respond     func(call int) ([]fantasy.StreamPart, error)
		wantErr     bool
		wantLogs    []string
		notWantLogs []string
	}{
		"plain reply": {
			respond: func(int) ([]fantasy.StreamPart, error) {
				return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1}), nil
			},
			notWantLogs: []string{contentFilterLog, flushLog},
		},
		"content filter": {
			respond: func(int) ([]fantasy.StreamPart, error) {
				return textReply("", fantasy.FinishReasonContentFilter, fantasy.Usage{InputTokens: 1}), nil
			},
			wantLogs:    []string{contentFilterLog},
			notWantLogs: []string{flushLog},
		},
		"retried request": {
			respond:     retryOnce,
			wantLogs:    []string{"Provider request failed, retrying"},
			notWantLogs: []string{resetLog, flushLog},
		},
		"failed stream": {
			respond: func(int) ([]fantasy.StreamPart, error) {
				return toolInputThenError("call_0", "work", errors.New("stream broke")), nil
			},
			wantErr:     true,
			notWantLogs: []string{syncLeafLog, flushLog},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			logs := &lockedBuffer{}
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			env := testEnv(t)
			sess := newTestSession(t, env)
			a := turnAgent(env, &recordingModel{respond: tc.respond}, "system", 200_000, okTool("work"))

			_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			got := logs.String()
			for _, want := range tc.wantLogs {
				require.Contains(t, got, want)
			}
			for _, notWant := range tc.notWantLogs {
				require.NotContains(t, got, notWant)
			}
		})
	}
}

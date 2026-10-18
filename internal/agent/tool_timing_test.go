package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/stretchr/testify/require"
)

// toolCallsThenStop replies with one step calling toolName once per ID,
// then a step that ends the turn.
func toolCallsThenStop(toolName string, ids ...string) func(int) ([]fantasy.StreamPart, error) {
	return func(call int) ([]fantasy.StreamPart, error) {
		if call > 0 {
			return []fantasy.StreamPart{
				{Type: fantasy.StreamPartTypeTextDelta, Delta: "done"},
				{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
			}, nil
		}
		var parts []fantasy.StreamPart
		for _, id := range ids {
			parts = append(parts,
				fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: id, ToolCallName: toolName},
				fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputEnd, ID: id},
				fantasy.StreamPart{Type: fantasy.StreamPartTypeToolCall, ID: id, ToolCallName: toolName, ToolCallInput: "{}"},
			)
		}
		return append(parts, fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls}), nil
	}
}

// persistedCall reads the tool call with the given ID from the assistant
// message the running tool belongs to.
func persistedCall(ctx context.Context, t *testing.T, env fakeEnv, callID string) message.ToolCall {
	t.Helper()
	msgID, _ := ctx.Value(tools.MessageIDContextKey).(string)
	msg, err := env.messages.Get(ctx, msgID)
	require.NoError(t, err)
	for _, tc := range msg.ToolCalls() {
		if tc.ID == callID {
			return tc
		}
	}
	t.Fatalf("tool call %s not found", callID)
	return message.ToolCall{}
}

func toolResultsByID(t *testing.T, env fakeEnv, sessionID string) map[string]message.ToolResult {
	t.Helper()
	got := make(map[string]message.ToolResult)
	for _, msg := range listMessages(t, env, sessionID, message.Tool) {
		for _, tr := range msg.ToolResults() {
			got[tr.ToolCallID] = tr
		}
	}
	return got
}

func toolCallsByID(t *testing.T, env fakeEnv, sessionID string) map[string]message.ToolCall {
	t.Helper()
	got := make(map[string]message.ToolCall)
	for _, msg := range listMessages(t, env, sessionID, message.Assistant) {
		for _, tc := range msg.ToolCalls() {
			got[tc.ID] = tc
		}
	}
	return got
}

func TestRunRecordsToolStartBeforeItRunsAndFinishWithItsResult(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess := newTestSession(t, env)

	var streamed time.Time
	model := &recordingModel{respond: func(call int) ([]fantasy.StreamPart, error) {
		if call == 0 {
			streamed = time.Now()
		}
		return toolCallsThenStop("work", "call_0")(call)
	}}
	var seenAtEntry message.ToolCall
	var entered, returned time.Time
	tool := stepTool("work", func(ctx context.Context, _ int) fantasy.ToolResponse {
		entered = time.Now()
		seenAtEntry = persistedCall(ctx, t, env, "call_0")
		returned = time.Now()
		return fantasy.NewTextResponse("ok")
	})
	a := turnAgent(env, model, "system", 200_000, tool)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)
	after := time.Now()

	require.NotZero(t, seenAtEntry.StartedAt, "the start is persisted before the tool body runs")
	require.GreaterOrEqual(t, seenAtEntry.StartedAt, streamed.UnixMilli())
	require.LessOrEqual(t, seenAtEntry.StartedAt, entered.UnixMilli())
	require.Equal(t, seenAtEntry.StartedAt, toolCallsByID(t, env, sess.ID)["call_0"].StartedAt)

	result := toolResultsByID(t, env, sess.ID)["call_0"]
	require.GreaterOrEqual(t, result.FinishedAt, returned.UnixMilli())
	require.LessOrEqual(t, result.FinishedAt, after.UnixMilli())
}

func TestRunStartsQueuedSequentialToolWhenItRuns(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess := newTestSession(t, env)

	model := &recordingModel{respond: toolCallsThenStop("work", "call_0", "call_1")}
	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 0 {
			time.Sleep(20 * time.Millisecond)
		}
		return fantasy.NewTextResponse("ok")
	})
	a := turnAgent(env, model, "system", 200_000, tool)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)

	calls := toolCallsByID(t, env, sess.ID)
	results := toolResultsByID(t, env, sess.ID)
	require.NotZero(t, calls["call_0"].StartedAt)
	require.GreaterOrEqual(t, calls["call_1"].StartedAt, results["call_0"].FinishedAt,
		"the queued call's clock starts after the call ahead of it finishes")
}

func TestRunRestartsToolClockWhenHumanGrantsPermission(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	env.permissions = permission.NewPermissionService(env.workingDir, config.YoloOff, nil, nil)
	sess := newTestSession(t, env)
	prompts := env.permissions.Subscribe(t.Context())

	model := &recordingModel{respond: toolCallsThenStop("work", "call_0")}
	var beforePrompt message.ToolCall
	tool := stepTool("work", func(ctx context.Context, _ int) fantasy.ToolResponse {
		beforePrompt = persistedCall(ctx, t, env, "call_0")
		res, err := env.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:  sess.ID,
			ToolCallID: "call_0",
			ToolName:   "work",
			Action:     "execute",
			Path:       env.workingDir,
		})
		require.NoError(t, err)
		require.True(t, res.Granted)
		return fantasy.NewTextResponse("ok")
	})
	a := turnAgent(env, model, "system", 200_000, tool)

	var grantedAfter time.Time
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case ev := <-prompts:
			time.Sleep(5 * time.Millisecond)
			grantedAfter = time.Now()
			env.permissions.Grant(ev.Payload)
		case <-time.After(10 * time.Second):
			t.Error("permission prompt was not published")
		}
	})

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)
	wg.Wait()

	restarted := toolCallsByID(t, env, sess.ID)["call_0"].StartedAt
	require.NotZero(t, beforePrompt.StartedAt)
	require.Greater(t, restarted, beforePrompt.StartedAt)
	require.GreaterOrEqual(t, restarted, grantedAfter.UnixMilli())
}

func TestRunFailureStampsSyntheticResultsWithFinishTime(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess := newTestSession(t, env)
	model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
		return toolInputThenError("call_0", "work", fmt.Errorf("stream broke")), nil
	}}
	a := turnAgent(env, model, "system", 200_000, okTool("work"))

	before := time.Now()
	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.Error(t, err)
	after := time.Now()

	result := toolResultsByID(t, env, sess.ID)["call_0"]
	require.GreaterOrEqual(t, result.FinishedAt, before.UnixMilli())
	require.LessOrEqual(t, result.FinishedAt, after.UnixMilli())
}

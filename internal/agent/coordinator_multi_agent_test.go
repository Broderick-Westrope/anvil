package agent

import (
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInjectBackgroundTaskResults verifies that injectBackgroundTaskResults
// writes proper assistant tool-call + tool result message pairs into the DB.
func TestInjectBackgroundTaskResults(t *testing.T) {
	t.Parallel()

	env := testEnv(t)

	sess, err := env.sessions.Create(t.Context(), "inject-test-session")
	require.NoError(t, err)

	coord := &coordinator{
		messages: env.messages,
	}

	results := []BackgroundTaskResult{
		{
			TaskID:    "task-success-abc123",
			AgentName: "coder",
			Result:    "Task completed successfully.",
			Success:   true,
		},
		{
			TaskID:    "task-failure-def456",
			AgentName: "reviewer",
			Result:    "Something went wrong.",
			Success:   false,
		},
	}

	err = coord.injectBackgroundTaskResults(t.Context(), sess.ID, results)
	require.NoError(t, err)

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)

	// Expect 4 messages: 2 assistant (tool call) + 2 tool (tool result).
	require.Len(t, msgs, 4, "expected 4 messages (2 pairs of assistant + tool)")

	// --- First pair: success result ---
	assistantMsg := msgs[0]
	assert.Equal(t, message.Assistant, assistantMsg.Role)
	toolCalls := assistantMsg.ToolCalls()
	require.Len(t, toolCalls, 1)
	assert.Equal(t, "background_task_result", toolCalls[0].Name)
	assert.Equal(t, "bg_task-success-abc123", toolCalls[0].ID)

	toolMsg := msgs[1]
	assert.Equal(t, message.Tool, toolMsg.Role)
	toolResults := toolMsg.ToolResults()
	require.Len(t, toolResults, 1)
	assert.Equal(t, "background_task_result", toolResults[0].Name)
	assert.Equal(t, "bg_task-success-abc123", toolResults[0].ToolCallID)
	assert.Equal(t, "Task completed successfully.", toolResults[0].Content)
	assert.False(t, toolResults[0].IsError)

	// --- Second pair: failure result ---
	assistantMsg2 := msgs[2]
	assert.Equal(t, message.Assistant, assistantMsg2.Role)
	toolCalls2 := assistantMsg2.ToolCalls()
	require.Len(t, toolCalls2, 1)
	assert.Equal(t, "background_task_result", toolCalls2[0].Name)
	assert.Equal(t, "bg_task-failure-def456", toolCalls2[0].ID)

	toolMsg2 := msgs[3]
	assert.Equal(t, message.Tool, toolMsg2.Role)
	toolResults2 := toolMsg2.ToolResults()
	require.Len(t, toolResults2, 1)
	assert.Equal(t, "background_task_result", toolResults2[0].Name)
	assert.Equal(t, "bg_task-failure-def456", toolResults2[0].ToolCallID)
	assert.Equal(t, "Something went wrong.", toolResults2[0].Content)
	assert.True(t, toolResults2[0].IsError)
}

// TestInjectBackgroundTaskResults_EmptyResult verifies that a failed task
// with an empty result string gets the default failure message.
func TestInjectBackgroundTaskResults_EmptyResult(t *testing.T) {
	t.Parallel()

	env := testEnv(t)

	sess, err := env.sessions.Create(t.Context(), "inject-empty-test-session")
	require.NoError(t, err)

	coord := &coordinator{
		messages: env.messages,
	}

	results := []BackgroundTaskResult{
		{
			TaskID:    "task-fail-no-msg-xyz",
			AgentName: "coder",
			Result:    "",
			Success:   false,
		},
	}

	err = coord.injectBackgroundTaskResults(t.Context(), sess.ID, results)
	require.NoError(t, err)

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)

	toolResults := msgs[1].ToolResults()
	require.Len(t, toolResults, 1)
	assert.Equal(t, "Background task failed", toolResults[0].Content)
	assert.True(t, toolResults[0].IsError)
}

// TestDrainPendingResults verifies that DrainPendingResults correctly drains
// and clears the queue, and returns nil when nothing is pending.
func TestDrainPendingResults(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when no results pending", func(t *testing.T) {
		t.Parallel()
		c := &coordinator{}
		got := c.DrainPendingResults()
		assert.Nil(t, got)
	})

	t.Run("drains and clears the queue", func(t *testing.T) {
		t.Parallel()
		c := &coordinator{
			pendingResults: []BackgroundTaskResult{
				{TaskID: "a", AgentName: "alpha", Result: "ok", Success: true},
				{TaskID: "b", AgentName: "beta", Result: "fail", Success: false},
			},
		}

		got := c.DrainPendingResults()
		require.Len(t, got, 2)
		assert.Equal(t, "a", got[0].TaskID)
		assert.Equal(t, "b", got[1].TaskID)

		// Second drain must return nil.
		got2 := c.DrainPendingResults()
		assert.Nil(t, got2)
	})

	t.Run("concurrent drain safety", func(t *testing.T) {
		t.Parallel()
		c := &coordinator{}

		// Pre-populate with a known number of results.
		for i := range 50 {
			c.pendingResults = append(c.pendingResults, BackgroundTaskResult{
				TaskID:  string(rune('a' + i)),
				Success: true,
			})
		}

		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			drained []BackgroundTaskResult
		)
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results := c.DrainPendingResults()
				if len(results) > 0 {
					mu.Lock()
					drained = append(drained, results...)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		// All 50 pre-populated results should have been drained exactly once.
		assert.Len(t, drained, 50)
	})
}

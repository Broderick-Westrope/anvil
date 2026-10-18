package agent

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

const testProvider = "test-provider"

var subagentIDLine = regexp.MustCompile(`\n\nsubagent_id: fixer-[0-9a-f]{4}$`)

// newSubagentCallCoordinator returns a coordinator whose task calls back
// off for a millisecond between automatic resumes.
func newSubagentCallCoordinator(t *testing.T) (*coordinator, fakeEnv, session.Session) {
	t.Helper()
	env := testEnv(t)
	c := newTestCoordinator(t, env, testProvider, config.ProviderConfig{ID: testProvider})
	c.subagentRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
	require.NoError(t, err)
	return c, env, parent
}

func fixerParams(parentID, toolCallID string, agent SessionAgent) subAgentParams {
	return subAgentParams{
		Agent:          agent,
		AgentType:      "fixer",
		Depth:          2,
		SessionID:      parentID,
		AgentMessageID: "msg-1",
		ToolCallID:     toolCallID,
		Prompt:         "fix it",
		SessionTitle:   "Fix",
	}
}

func overloaded() error {
	return fmt.Errorf("stream: %w", &fantasy.ProviderError{StatusCode: 503, Title: "overloaded"})
}

func TestTaskResultEndsWithSubagentID(t *testing.T) {
	t.Parallel()

	tests := map[string]func(ctx context.Context, cancel context.CancelFunc) (*fantasy.AgentResult, error){
		"success": func(context.Context, context.CancelFunc) (*fantasy.AgentResult, error) {
			return agentResultWithText("done"), nil
		},
		"permanent error": func(context.Context, context.CancelFunc) (*fantasy.AgentResult, error) {
			return nil, &fantasy.ProviderError{StatusCode: 400, Title: "bad request"}
		},
		"parent cancel": func(ctx context.Context, cancel context.CancelFunc) (*fantasy.AgentResult, error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		},
		"no output": func(context.Context, context.CancelFunc) (*fantasy.AgentResult, error) {
			return &fantasy.AgentResult{}, nil
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, _, parent := newSubagentCallCoordinator(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			agent := newMockAgent(testProvider, 1000, func(ctx context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
				return run(ctx, cancel)
			})
			resp, err := c.runSubAgent(ctx, fixerParams(parent.ID, "call-1", agent))
			require.NoError(t, err)
			require.Regexp(t, subagentIDLine, resp.Content)
		})
	}
}

func TestSubagentIdentityIsStoredBeforeTheFirstStep(t *testing.T) {
	t.Parallel()
	c, env, parent := newSubagentCallCoordinator(t)

	agent := newMockAgent(testProvider, 1000, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		sub, err := env.sessions.GetSubagent(ctx, call.SessionID)
		require.NoError(t, err)
		require.Equal(t, session.Subagent{
			SessionID: call.SessionID, ParentSessionID: parent.ID, Handle: sub.Handle,
			Origin: session.SubagentOriginTask, AgentType: "fixer", Depth: 2, Provider: testProvider, Model: "m-1",
		}, sub)
		link, err := env.sessions.GetSubagentCall(ctx, "msg-1$$call-1")
		require.NoError(t, err)
		require.Equal(t, session.SubagentCall{ID: "msg-1$$call-1", SessionID: call.SessionID, Kind: session.SubagentCallNew}, link)
		return agentResultWithText("done"), nil
	})
	agent.model.ModelCfg.Model = "m-1"
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.False(t, resp.IsError)
}

func TestTransientFailureResumesInTheSameChildWithAHarnessMessage(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)

	var calls []SessionAgentCall
	agent := newMockAgent(testProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		calls = append(calls, call)
		if len(calls) == 1 {
			return nil, overloaded()
		}
		return agentResultWithText("recovered output"), nil
	})
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.False(t, resp.IsError)

	require.Len(t, calls, 2)
	require.Equal(t, calls[0].SessionID, calls[1].SessionID)
	require.Equal(t, "fix it", calls[0].Prompt)
	require.Equal(t, []message.InjectionContent{{
		Source: message.InjectionSourceHarness, Kind: message.InjectionKindRetry,
		Text: subagentContinueText, CallID: "msg-1$$call-1", Attempt: 1, MaxAttempts: 2, Reason: "503 overloaded",
	}}, calls[1].Injections)
	require.True(t, strings.HasPrefix(resp.Content, "recovered output\n\nrecovered after 1 transient failure: 503 overloaded"), resp.Content)
}

func TestTransientFailuresStopAfterTwoResumes(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)

	var runs atomic.Int32
	agent := newMockAgent(testProvider, 1000, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		runs.Add(1)
		return nil, overloaded()
	})
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, int32(3), runs.Load())
	require.Contains(t, resp.Content, "overloaded")
	require.Contains(t, resp.Content, "after 3 attempts")
	require.Regexp(t, subagentIDLine, resp.Content)
}

func TestPermanentFailuresAreNotRetried(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)

	var runs atomic.Int32
	agent := newMockAgent(testProvider, 1000, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		runs.Add(1)
		return nil, fmt.Errorf("x: %w", io.EOF)
	})
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, int32(1), runs.Load())
	require.NotContains(t, resp.Content, "attempts")
}

func TestEscDuringBackoffCancelsTheCallAsHuman(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)
	c.subagentRetryDelays = []time.Duration{time.Hour, time.Hour}

	failed := make(chan string, 1)
	agent := newMockAgent(testProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		failed <- call.SessionID
		return nil, overloaded()
	})
	go func() {
		childID := <-failed
		require.Eventually(t, func() bool { return c.CancelSubagent(childID) }, 5*time.Second, time.Millisecond)
	}()
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "cancelled by human")
	require.Regexp(t, subagentIDLine, resp.Content)
}

func TestParentCancelIsReportedAsSuch(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)
	ctx, cancel := context.WithCancel(t.Context())
	agent := newMockAgent(testProvider, 1000, func(ctx context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	})
	resp, err := c.runSubAgent(ctx, fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.Contains(t, resp.Content, "cancelled by parent or run cancel")
}

func TestSteerDuringTheFinalStepForcesAnotherTurn(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)

	var calls []SessionAgentCall
	agent := newMockAgent(testProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		calls = append(calls, call)
		if len(calls) == 1 {
			require.NoError(t, c.SteerSubagent(call.SessionID, "also update the docs", false))
			return agentResultWithText("first answer"), nil
		}
		return agentResultWithText("second answer"), nil
	})
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.Len(t, calls, 2)
	require.Equal(t, []message.InjectionContent{{
		Source: message.InjectionSourceHuman, Kind: message.InjectionKindSteer, Text: "also update the docs", CallID: "msg-1$$call-1",
	}}, calls[1].Injections)
	require.True(t, strings.HasPrefix(resp.Content, "second answer"))
}

func TestSteerAfterTheCallEndsIsRejected(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)
	var childID string
	agent := newMockAgent(testProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		childID = call.SessionID
		return agentResultWithText("done"), nil
	})
	_, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.ErrorIs(t, c.SteerSubagent(childID, "late", false), errSteerRejected)
	require.False(t, c.SubagentRunning(childID))
}

func TestInterruptSteerContinuesTheSameCall(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)

	var calls []SessionAgentCall
	agent := newMockAgent(testProvider, 1000, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		calls = append(calls, call)
		if len(calls) == 1 {
			require.NoError(t, c.SteerSubagent(call.SessionID, "stop, use v2", true))
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return agentResultWithText("v2 done"), nil
	})
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Len(t, calls, 2)
	require.Equal(t, message.InjectionKindInterruptSteer, calls[1].Injections[0].Kind)
	require.True(t, strings.HasPrefix(resp.Content, "v2 done"))
}

func TestPendingSteersAreHandedBackWhenTheCallFails(t *testing.T) {
	t.Parallel()

	tests := map[string]func(ctx context.Context, c *coordinator, call SessionAgentCall) error{
		"permanent error": func(context.Context, *coordinator, SessionAgentCall) error {
			return &fantasy.ProviderError{StatusCode: 400}
		},
		"exhausted retries": func(context.Context, *coordinator, SessionAgentCall) error {
			return overloaded()
		},
		"persistence failure": func(context.Context, *coordinator, SessionAgentCall) error {
			return persistenceError{io.ErrClosedPipe}
		},
		"cancel": func(ctx context.Context, c *coordinator, call SessionAgentCall) error {
			c.CancelSubagent(call.SessionID)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	for name, fail := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, _, parent := newSubagentCallCoordinator(t)

			var childID string
			agent := newMockAgent(testProvider, 1000, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
				childID = call.SessionID
				_ = c.SteerSubagent(call.SessionID, "never delivered", false)
				return nil, fail(ctx, c, call)
			})
			_, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
			require.NoError(t, err)

			returned := c.TakeReturnedSteers()
			require.Len(t, returned, 1)
			require.Equal(t, childID, returned[0].SessionID)
			require.Contains(t, returned[0].Steers, "never delivered")
			require.NotEmpty(t, returned[0].Reason)
			require.Empty(t, c.TakeReturnedSteers(), "taking them clears the queue")
		})
	}
}

func TestEachAttemptRollsUpOnlyNewCost(t *testing.T) {
	t.Parallel()
	c, env, parent := newSubagentCallCoordinator(t)

	attempt := 0
	agent := newMockAgent(testProvider, 1000, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		attempt++
		child, err := env.sessions.Get(ctx, call.SessionID)
		require.NoError(t, err)
		child.Cost += 0.10
		_, err = env.sessions.Save(ctx, child)
		require.NoError(t, err)
		if attempt == 1 {
			return nil, overloaded()
		}
		return agentResultWithText("done"), nil
	})
	_, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)

	updated, err := env.sessions.Get(t.Context(), parent.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.20, updated.Cost, 1e-9)
}

func TestTaskRunsAreAwaitedSoCancelReachesCompactionContinuations(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)
	agent := newMockAgent(testProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		require.True(t, call.Await)
		return agentResultWithText("done"), nil
	})
	_, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
}

func TestSteersTakenForATurnThatNeverStartedAreHandedBack(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)

	runs := 0
	agent := newMockAgent(testProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		runs++
		if runs == 1 {
			require.NoError(t, c.SteerSubagent(call.SessionID, "also X", false))
			return agentResultWithText("first"), nil
		}
		// The turn fails before it persists its injection message.
		return nil, persistenceError{io.ErrClosedPipe}
	})
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.True(t, resp.IsError)

	returned := c.TakeReturnedSteers()
	require.Len(t, returned, 1)
	require.Equal(t, []string{"also X"}, returned[0].Steers)
}

func TestInterruptDoesNotOverrideAPersistenceFailure(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)

	var runs atomic.Int32
	agent := newMockAgent(testProvider, 1000, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		runs.Add(1)
		require.NoError(t, c.SteerSubagent(call.SessionID, "stop", true))
		<-ctx.Done()
		return nil, persistenceError{io.ErrClosedPipe}
	})
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, int32(1), runs.Load())
	returned := c.TakeReturnedSteers()
	require.Len(t, returned, 1)
	require.Equal(t, []string{"stop"}, returned[0].Steers)
}

func TestNoTextOutputIsNotRetried(t *testing.T) {
	t.Parallel()
	c, _, parent := newSubagentCallCoordinator(t)
	var runs atomic.Int32
	agent := newMockAgent(testProvider, 1000, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		runs.Add(1)
		return &fantasy.AgentResult{}, nil
	})
	resp, err := c.runSubAgent(t.Context(), fixerParams(parent.ID, "call-1", agent))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, int32(1), runs.Load())
}

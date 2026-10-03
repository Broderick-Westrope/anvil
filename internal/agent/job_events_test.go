package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

const jobNoticeMarker = "Background job updates:"

// jobOwners is a mutable OwnerFunc backing for tests.
type jobOwners struct {
	mu     sync.Mutex
	owners map[string]string
}

func (o *jobOwners) set(jobID, sessionID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.owners[jobID] = sessionID
}

func (o *jobOwners) owner(jobID string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.owners[jobID]
	return s, ok
}

func newJobEventsStore() (*jobevents.Store, *jobOwners) {
	owners := &jobOwners{owners: make(map[string]string)}
	return jobevents.NewStore(owners.owner), owners
}

func jobEventsAgent(env fakeEnv, messages message.Service, model fantasy.LanguageModel, store *jobevents.Store, agentTools ...fantasy.AgentTool) SessionAgent {
	m := Model{
		Model:      model,
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000},
	}
	return NewSessionAgent(SessionAgentOptions{
		LargeModel:   m,
		SmallModel:   m,
		SystemPrompt: "system",
		IsYolo:       true,
		Sessions:     env.sessions,
		Messages:     messages,
		Tools:        agentTools,
		JobEvents:    store,
	})
}

// stepTool returns a tool named name that calls onCall with the
// zero-based call index and returns its response.
func stepTool(name string, onCall func(ctx context.Context, call int) fantasy.ToolResponse) fantasy.AgentTool {
	var calls atomic.Int64
	return fantasy.NewAgentTool(name, "Test tool.",
		func(ctx context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return onCall(ctx, int(calls.Add(1)-1)), nil
		})
}

func completedJob(jobID, sessionID string) shell.JobInfo {
	return shell.JobInfo{ID: jobID, SessionID: sessionID, Command: "make " + jobID, Done: true, ExitCode: 1}
}

// notices returns the texts of the job notices in a step's prompt.
func notices(prompt []fantasy.Message) []string {
	var out []string
	for _, msg := range prompt {
		if text := textOf(msg); msg.Role == fantasy.MessageRoleUser && strings.Contains(text, jobNoticeMarker) {
			out = append(out, text)
		}
	}
	return out
}

func runJobEventsSession(t *testing.T, a SessionAgent, sessionID string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := a.Run(ctx, SessionAgentCall{SessionID: sessionID, Prompt: "start", NonInteractive: true})
	return err
}

func TestJobEvents_CompletionVisibleInLaterSteps(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Job events", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)

	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 0 {
			store.JobCompleted(completedJob("J01", sess.ID), "FAIL x\nTests: 1 failed")
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &scriptedModel{toolName: "work", toolSteps: 3}
	require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, model, store, tool), sess.ID))

	require.Len(t, model.prompts, 4)
	require.Empty(t, notices(model.prompts[0]))
	for step := 1; step < 4; step++ {
		got := notices(model.prompts[step])
		require.Len(t, got, 1, "step %d", step)
		require.Contains(t, got[0], "- Job J01 completed, exit 1")
		require.Contains(t, got[0], "  Tests: 1 failed")
	}

	// The notice is persisted as a job_event message on the branch.
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var jobEventMsgs int
	for _, msg := range msgs {
		if msg.MessageType == message.MessageTypeJobEvent {
			jobEventMsgs++
			require.Equal(t, message.User, msg.Role)
		}
	}
	require.Equal(t, 1, jobEventMsgs)
	require.False(t, store.HasPending(sess.ID))
}

func TestJobEvents_ObservedCompletionNotNotified(t *testing.T) {
	t.Parallel()

	doneMeta := func() fantasy.ToolResponse {
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse("Status: completed"),
			tools.JobOutputResponseMetadata{ShellID: "J01", Done: true})
	}
	tests := []struct {
		name   string
		onCall func(store *jobevents.Store, sessionID string, call int) fantasy.ToolResponse
	}{
		{
			name: "event then observation",
			onCall: func(store *jobevents.Store, sessionID string, call int) fantasy.ToolResponse {
				if call == 0 {
					store.JobCompleted(completedJob("J01", sessionID), "")
					return doneMeta()
				}
				return fantasy.NewTextResponse("ok")
			},
		},
		{
			name: "observation persisted before event",
			onCall: func(store *jobevents.Store, sessionID string, call int) fantasy.ToolResponse {
				switch call {
				case 0:
					return doneMeta()
				case 1:
					store.JobCompleted(completedJob("J01", sessionID), "")
				}
				return fantasy.NewTextResponse("ok")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := testEnv(t)
			sess, err := env.sessions.Create(t.Context(), "Observed", t.TempDir())
			require.NoError(t, err)
			store, owners := newJobEventsStore()
			owners.set("J01", sess.ID)

			tool := stepTool(tools.JobOutputToolName, func(_ context.Context, call int) fantasy.ToolResponse {
				return tt.onCall(store, sess.ID, call)
			})
			model := &scriptedModel{toolName: tools.JobOutputToolName, toolSteps: 2}
			require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, model, store, tool), sess.ID))

			require.Len(t, model.prompts, 3)
			for step, prompt := range model.prompts {
				require.Empty(t, notices(prompt), "step %d", step)
			}
			require.False(t, store.HasPending(sess.ID))
		})
	}
}

func TestJobEvents_ObservedMatchNotNotified(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		watchGen uint64
		notified bool
	}{
		{name: "wait match after the watch fired", watchGen: 1, notified: false},
		{name: "wait match before the watch fired", watchGen: 0, notified: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := testEnv(t)
			sess, err := env.sessions.Create(t.Context(), "Observed match", t.TempDir())
			require.NoError(t, err)
			store, owners := newJobEventsStore()
			owners.set("J01", sess.ID)
			info := shell.JobInfo{ID: "J01", SessionID: sess.ID, Command: "serve"}

			tool := stepTool(tools.JobOutputToolName, func(_ context.Context, call int) fantasy.ToolResponse {
				if call > 0 {
					return fantasy.NewTextResponse("ok")
				}
				store.WatchReplaced("J01", 1)
				store.PatternMatched(info, 1, "ready")
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(`Status: running, matched "ready"`),
					tools.JobOutputResponseMetadata{
						ShellID:     "J01",
						EndReason:   string(shell.WaitMatched),
						MatchedLine: "ready",
						WatchGen:    tt.watchGen,
					})
			})
			model := &scriptedModel{toolName: tools.JobOutputToolName, toolSteps: 1}
			require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, model, store, tool), sess.ID))

			require.Len(t, model.prompts, 2)
			got := notices(model.prompts[1])
			if tt.notified {
				require.Len(t, got, 1)
				require.Contains(t, got[0], "J01")
			} else {
				require.Empty(t, got)
			}
			require.False(t, store.HasPending(sess.ID))
		})
	}
}

func TestJobEvents_KillExitObservesCompletion(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Kill", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)
	owners.set("J02", sess.ID)

	tool := stepTool(tools.JobKillToolName, func(_ context.Context, call int) fantasy.ToolResponse {
		if call > 0 {
			return fantasy.NewTextResponse("ok")
		}
		store.JobCompleted(completedJob("J01", sess.ID), "")
		store.JobCompleted(completedJob("J02", sess.ID), "")
		// J01's kill was confirmed; J02 is reported as abandoned.
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse("terminated"),
			tools.JobKillResponseMetadata{ShellID: "J01", Exited: true})
	})
	model := &scriptedModel{toolName: tools.JobKillToolName, toolSteps: 1}
	require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, model, store, tool), sess.ID))

	require.Len(t, model.prompts, 2)
	got := notices(model.prompts[1])
	require.Len(t, got, 1)
	require.Contains(t, got[0], "Job J02 completed")
	require.NotContains(t, got[0], "Job J01")
}

func TestJobEvents_SixEventsSplitAcrossSteps(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Six events", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	for i := 1; i <= 6; i++ {
		owners.set(fmt.Sprintf("J%02d", i), sess.ID)
	}

	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 0 {
			for i := 1; i <= 6; i++ {
				store.JobCompleted(completedJob(fmt.Sprintf("J%02d", i), sess.ID), "")
			}
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &scriptedModel{toolName: "work", toolSteps: 2}
	require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, model, store, tool), sess.ID))

	require.Len(t, model.prompts, 3)
	step1 := notices(model.prompts[1])
	require.Len(t, step1, 1)
	require.Equal(t, 5, strings.Count(step1[0], " completed, exit "))
	require.Contains(t, step1[0], "(+1 more")
	require.NotContains(t, step1[0], "Job J06")

	step2 := notices(model.prompts[2])
	require.Len(t, step2, 2)
	require.Equal(t, step1[0], step2[0])
	require.Contains(t, step2[1], "Job J06 completed")
	require.NotContains(t, step2[1], "more pending")
}

// failingJobEventMessages fails Create for job_event messages.
type failingJobEventMessages struct {
	message.Service
}

var errJobEventCreate = errors.New("job event create failed")

func (f failingJobEventMessages) Create(ctx context.Context, sessionID string, params message.CreateMessageParams) (message.Message, error) {
	if params.MessageType == message.MessageTypeJobEvent {
		return message.Message{}, errJobEventCreate
	}
	return f.Service.Create(ctx, sessionID, params)
}

func TestJobEvents_CreateFailureLeavesEventsPending(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Create failure", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)

	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 0 {
			store.JobCompleted(completedJob("J01", sess.ID), "")
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &scriptedModel{toolName: "work", toolSteps: 2}
	a := jobEventsAgent(env, failingJobEventMessages{env.messages}, model, store, tool)
	err = runJobEventsSession(t, a, sess.ID)
	require.ErrorIs(t, err, errJobEventCreate)
	require.True(t, store.HasPending(sess.ID))
}

func TestJobEvents_HandedOffJobNotifiesNewOwner(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
	require.NoError(t, err)
	child, err := env.sessions.Create(t.Context(), "Child", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", child.ID)

	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		switch call {
		case 0:
			store.JobCompleted(completedJob("J01", child.ID), "")
		case 1:
			owners.set("J01", parent.ID)
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &scriptedModel{toolName: "work", toolSteps: 2}
	require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, model, store, tool), parent.ID))

	require.Len(t, model.prompts, 3)
	require.Empty(t, notices(model.prompts[1]), "the child still owned the job at step 1")
	got := notices(model.prompts[2])
	require.Len(t, got, 1)
	require.Contains(t, got[0], "Job J01 completed")

	childModel := &scriptedModel{}
	require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, childModel, store), child.ID))
	require.Len(t, childModel.prompts, 1)
	require.Empty(t, notices(childModel.prompts[0]))
}

func TestJobEvents_CancelKeepsPendingEvents(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Cancel", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)

	var a SessionAgent
	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 0 {
			store.JobCompleted(completedJob("J01", sess.ID), "")
			a.Cancel(sess.ID)
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &scriptedModel{toolName: "work", toolSteps: 2}
	a = jobEventsAgent(env, env.messages, model, store, tool)

	require.Error(t, runJobEventsSession(t, a, sess.ID))
	require.Len(t, model.prompts, 1)
	require.True(t, store.HasPending(sess.ID))

	// The next run delivers the event at its first step.
	next := &scriptedModel{}
	require.NoError(t, runJobEventsSession(t, jobEventsAgent(env, env.messages, next, store), sess.ID))
	require.Len(t, next.prompts, 1)
	got := notices(next.prompts[0])
	require.Len(t, got, 1)
	require.Contains(t, got[0], "Job J01 completed")
	require.False(t, store.HasPending(sess.ID))
}

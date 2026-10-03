package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// dispatchModel answers a user message with a call to toolName (when
// set) and anything else with text. It records every prompt and the
// highest number of concurrent streams.
type dispatchModel struct {
	toolName string

	mu          sync.Mutex
	prompts     [][]fantasy.Message
	inFlight    int
	maxInFlight int
	calls       int
}

func (m *dispatchModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, cloneFantasyMessages(call.Prompt))
	m.inFlight++
	m.maxInFlight = max(m.maxInFlight, m.inFlight)
	m.calls++
	id := fmt.Sprintf("call_%d", m.calls)
	m.mu.Unlock()

	useTool := m.toolName != "" && len(call.Prompt) > 0 &&
		call.Prompt[len(call.Prompt)-1].Role == fantasy.MessageRoleUser
	parts := []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeTextStart, ID: "text"},
		{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: "done"},
		{Type: fantasy.StreamPartTypeTextEnd, ID: "text"},
		{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
	}
	if useTool {
		parts = []fantasy.StreamPart{
			{Type: fantasy.StreamPartTypeToolInputStart, ID: id, ToolCallName: m.toolName},
			{Type: fantasy.StreamPartTypeToolInputEnd, ID: id},
			{Type: fantasy.StreamPartTypeToolCall, ID: id, ToolCallName: m.toolName, ToolCallInput: "{}"},
			{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls},
		}
	}
	return iter.Seq[fantasy.StreamPart](func(yield func(fantasy.StreamPart) bool) {
		defer func() {
			m.mu.Lock()
			m.inFlight--
			m.mu.Unlock()
		}()
		for _, part := range parts {
			if !yield(part) {
				return
			}
		}
	}), nil
}

func (m *dispatchModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *dispatchModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *dispatchModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *dispatchModel) Provider() string { return "scripted" }
func (m *dispatchModel) Model() string    { return "scripted" }

func (m *dispatchModel) snapshot() ([][]fantasy.Message, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]fantasy.Message(nil), m.prompts...), m.maxInFlight
}

func (m *dispatchModel) promptCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prompts)
}

func jobEventsModel(model fantasy.LanguageModel) Model {
	return Model{
		Model:      model,
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000},
	}
}

func dispatchCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestDispatch_ConcurrentPromptsAndJobEvents(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Dispatch", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)
	owners.set("J02", sess.ID)

	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		switch call {
		case 0:
			store.JobCompleted(completedJob("J01", sess.ID), "")
		case 1:
			store.JobCompleted(completedJob("J02", sess.ID), "")
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &dispatchModel{toolName: "work"}
	a := jobEventsAgent(env, env.messages, model, store, tool)
	ctx := dispatchCtx(t)

	prompts := []string{"prompt-0-0", "prompt-0-1", "prompt-1-0", "prompt-1-1"}
	start := make(chan struct{})
	errs := make(chan error, len(prompts))
	var wg sync.WaitGroup
	for g := range 2 {
		wg.Go(func() {
			<-start
			for i := range 2 {
				_, runErr := a.Run(ctx, SessionAgentCall{
					SessionID:      sess.ID,
					Prompt:         fmt.Sprintf("prompt-%d-%d", g, i),
					NonInteractive: true,
				})
				errs <- runErr
			}
		})
	}
	close(start)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("runs did not finish")
	}
	close(errs)
	for runErr := range errs {
		require.NoError(t, runErr)
	}
	require.False(t, a.IsSessionBusy(sess.ID))
	require.Zero(t, a.QueuedPrompts(sess.ID))

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var userTexts, noticeTexts []string
	for _, msg := range msgs {
		if msg.Role != message.User {
			continue
		}
		if msg.MessageType == message.MessageTypeJobEvent {
			noticeTexts = append(noticeTexts, msg.Content().Text)
		} else {
			userTexts = append(userTexts, msg.Content().Text)
		}
	}
	require.ElementsMatch(t, prompts, userTexts)
	allNotices := strings.Join(noticeTexts, "\n")
	require.Equal(t, 1, strings.Count(allNotices, "Job J01 completed"))
	require.Equal(t, 1, strings.Count(allNotices, "Job J02 completed"))

	seen, maxInFlight := model.snapshot()
	require.Equal(t, 1, maxInFlight, "two streams ran concurrently for one session")
	var inputs strings.Builder
	for _, prompt := range seen {
		for _, msg := range prompt {
			if msg.Role == fantasy.MessageRoleUser {
				inputs.WriteString(textOf(msg))
				inputs.WriteString("\n")
			}
		}
	}
	for _, p := range prompts {
		require.Contains(t, inputs.String(), p)
	}
	require.Contains(t, inputs.String(), "Job J01 completed")
	require.Contains(t, inputs.String(), "Job J02 completed")
	require.False(t, store.HasPending(sess.ID))
	requireNoDispatchLocks(t, a)
}

func requireNoDispatchLocks(t *testing.T, a SessionAgent) {
	t.Helper()
	sa := a.(*sessionAgent)
	sa.dispatchLocksMu.Lock()
	defer sa.dispatchLocksMu.Unlock()
	require.Empty(t, sa.dispatchLocks, "dispatch locks must be released once idle")
}

func TestDispatch_LockDispatchExcludesAndCleansUp(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	var (
		wg      sync.WaitGroup
		holders atomic.Int64
		maxHeld atomic.Int64
	)
	start := make(chan struct{})
	for i := range 32 {
		wg.Go(func() {
			<-start
			sessionID := fmt.Sprintf("s%d", i%2)
			for range 50 {
				unlock := a.lockDispatch(sessionID)
				if sessionID == "s0" {
					n := holders.Add(1)
					if n > maxHeld.Load() {
						maxHeld.Store(n)
					}
					holders.Add(-1)
				}
				unlock()
			}
		})
	}
	close(start)
	wg.Wait()
	require.Equal(t, int64(1), maxHeld.Load())
	requireNoDispatchLocks(t, a)
}

func allowWake() bool { return true }

func TestDispatch_RunWakeDeliversNotice(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Wake", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)
	model := &dispatchModel{}
	a := jobEventsAgent(env, env.messages, model, store)
	ctx := dispatchCtx(t)

	// Nothing pending: no run, and the budget is untouched.
	for range maxConsecutiveWakes + 1 {
		res, wakeErr := a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, allowWake)
		require.NoError(t, wakeErr)
		require.Nil(t, res)
	}
	require.Zero(t, model.promptCount())

	store.JobCompleted(completedJob("J01", sess.ID), "")
	_, err = a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, func() bool { return false })
	require.ErrorIs(t, err, ErrWakeNotAllowed)
	require.True(t, store.HasPending(sess.ID))

	res, err := a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, allowWake)
	require.NoError(t, err)
	require.NotNil(t, res)
	prompts, _ := model.snapshot()
	require.Len(t, prompts, 1)
	last := prompts[0][len(prompts[0])-1]
	require.Equal(t, fantasy.MessageRoleUser, last.Role)
	require.Contains(t, textOf(last), "Job J01 completed")
	require.False(t, store.HasPending(sess.ID))

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, message.MessageTypeJobEvent, msgs[0].MessageType)
	require.Equal(t, message.Assistant, msgs[1].Role)
}

func TestDispatch_RunWakeBudget(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Wake budget", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	model := &dispatchModel{}
	a := jobEventsAgent(env, env.messages, model, store)
	ctx := dispatchCtx(t)

	complete := func(n int) {
		id := fmt.Sprintf("J%02d", n)
		owners.set(id, sess.ID)
		store.JobCompleted(completedJob(id, sess.ID), "")
	}
	for i := range maxConsecutiveWakes {
		complete(i)
		_, wakeErr := a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, allowWake)
		require.NoError(t, wakeErr, "wake %d", i)
	}
	complete(10)
	_, err = a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, allowWake)
	require.ErrorIs(t, err, ErrWakeNotAllowed)
	require.True(t, store.HasPending(sess.ID))

	// A user run delivers the pending event and resets the budget.
	_, err = a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "hi", NonInteractive: true})
	require.NoError(t, err)
	require.False(t, store.HasPending(sess.ID))
	complete(11)
	_, err = a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, allowWake)
	require.NoError(t, err)
	require.False(t, store.HasPending(sess.ID))
}

func TestDispatch_CancelSuppressesWakesUntilUserRun(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Wake cancel", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)
	owners.set("J02", sess.ID)
	model := &dispatchModel{}
	a := jobEventsAgent(env, env.messages, model, store)
	ctx := dispatchCtx(t)

	a.Cancel(sess.ID)
	store.JobCompleted(completedJob("J01", sess.ID), "")
	_, err = a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, allowWake)
	require.ErrorIs(t, err, ErrWakeNotAllowed)
	require.Zero(t, model.promptCount())

	_, err = a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "hi", NonInteractive: true})
	require.NoError(t, err)
	store.JobCompleted(completedJob("J02", sess.ID), "")
	res, err := a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, allowWake)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, store.HasPending(sess.ID))
}

func TestDispatch_RunWakeBusy(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Wake busy", t.TempDir())
	require.NoError(t, err)
	store, owners := newJobEventsStore()
	owners.set("J01", sess.ID)

	var a SessionAgent
	var wakeErr atomic.Value
	var idle atomic.Int64
	ctx := dispatchCtx(t)
	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 0 {
			_, err := a.RunWake(ctx, SessionAgentCall{SessionID: sess.ID, NonInteractive: true}, allowWake)
			wakeErr.Store(err)
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &dispatchModel{toolName: "work"}
	a = NewSessionAgent(SessionAgentOptions{
		LargeModel:   jobEventsModel(model),
		SmallModel:   jobEventsModel(model),
		SystemPrompt: "system",
		IsYolo:       true,
		Sessions:     env.sessions,
		Messages:     env.messages,
		Tools:        []fantasy.AgentTool{tool},
		JobEvents:    store,
		OnIdle: func(sessionID string) {
			if sessionID == sess.ID {
				idle.Add(1)
			}
		},
	})

	_, err = a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "start", NonInteractive: true})
	require.NoError(t, err)
	require.ErrorIs(t, wakeErr.Load().(error), ErrSessionBusy)
	require.Equal(t, int64(1), idle.Load(), "OnIdle fires once when the run ends with nothing queued")
}

// gatedSessions fails its first Get once gate is closed, after
// reporting on entered that the call started.
type gatedSessions struct {
	session.Service
	entered chan struct{}
	gate    chan struct{}
	calls   atomic.Int64
}

func (s *gatedSessions) Get(ctx context.Context, id string) (session.Session, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.gate
		return session.Session{}, errors.New("database unavailable")
	}
	return s.Service.Get(ctx, id)
}

func TestDispatch_SetupFailureRunsQueuedPrompt(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Setup failure", t.TempDir())
	require.NoError(t, err)
	sessions := &gatedSessions{Service: env.sessions, entered: make(chan struct{}), gate: make(chan struct{})}
	model := &dispatchModel{}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:   jobEventsModel(model),
		SmallModel:   jobEventsModel(model),
		SystemPrompt: "system",
		IsYolo:       true,
		Sessions:     sessions,
		Messages:     env.messages,
	})
	ctx := dispatchCtx(t)

	firstErr := make(chan error, 1)
	go func() {
		_, runErr := a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "first", NonInteractive: true})
		firstErr <- runErr
	}()
	select {
	case <-sessions.entered:
	case <-ctx.Done():
		t.Fatal("first run did not start")
	}

	res, err := a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "queued", NonInteractive: true})
	require.NoError(t, err)
	require.Nil(t, res)
	require.Equal(t, 1, a.QueuedPrompts(sess.ID))
	close(sessions.gate)

	select {
	case err = <-firstErr:
	case <-ctx.Done():
		t.Fatal("first run did not return")
	}
	require.ErrorContains(t, err, "database unavailable")
	require.Zero(t, a.QueuedPrompts(sess.ID))
	require.False(t, a.IsSessionBusy(sess.ID))
	require.Equal(t, 1, model.promptCount(), "the queued prompt must run")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	require.Equal(t, "queued", msgs[0].Content().Text)
}

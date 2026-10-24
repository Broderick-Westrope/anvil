package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

// fakeLanguageModel is a [fantasy.LanguageModel] stub that records the
// context its methods were called with and can be configured with a custom
// stream body.
type fakeLanguageModel struct {
	generateCtx context.Context
	streamCtx   context.Context
	stream      func(yield func(fantasy.StreamPart) bool)
}

func (f *fakeLanguageModel) Generate(ctx context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	f.generateCtx = ctx
	return &fantasy.Response{}, nil
}

func (f *fakeLanguageModel) Stream(ctx context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	f.streamCtx = ctx
	if f.stream == nil {
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{})
		}, nil
	}
	return f.stream, nil
}

func (f *fakeLanguageModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return &fantasy.ObjectResponse{}, nil
}

func (f *fakeLanguageModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}

func (f *fakeLanguageModel) Provider() string { return "fake" }
func (f *fakeLanguageModel) Model() string    { return "fake-model" }

func TestNewRequestTimeoutModel_Disabled(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	require.Same(t, inner, newRequestTimeoutModel(inner, 0))
	require.Same(t, inner, newRequestTimeoutModel(inner, -time.Second))
}

func TestRequestTimeoutModel_GenerateDeadline(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	m := newRequestTimeoutModel(inner, 5*time.Minute)

	_, err := m.Generate(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	_, ok := inner.generateCtx.Deadline()
	require.True(t, ok, "Generate should run under a deadline")
}

func TestRequestTimeoutModel_StreamDeadlineOutlivesCall(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	m := newRequestTimeoutModel(inner, 5*time.Minute)

	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	require.NoError(t, inner.streamCtx.Err(), "the idle timer must not fire while the stream is being consumed")

	for range stream {
	}

	require.ErrorIs(t, inner.streamCtx.Err(), context.Canceled, "the stream context should be released after the stream ends")
}

func TestRequestTimeoutModel_StreamAbortsWhenIdle(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A stream that outlives the idle window: it waits for the context to
	// be done and reports what it observed.
	streamObserved := make(chan error, 1)
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		streamObserved <- inner.streamCtx.Err()
	}
	m := newRequestTimeoutModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream {
		}
	}()

	select {
	case err := <-streamObserved:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("stream was not aborted by the idle timeout")
	}
	<-done
}

func TestRequestTimeoutModel_ActiveStreamSurvives(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A stream that keeps sending data: total runtime exceeds the timeout,
	// but every gap is shorter than the idle window, so it must finish.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		for range 6 {
			time.Sleep(10 * time.Millisecond)
			if !yield(fantasy.StreamPart{}) {
				return
			}
		}
	}
	m := newRequestTimeoutModel(inner, 25*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	parts := 0
	for part := range stream {
		require.NoError(t, part.Error)
		parts++
	}
	require.Equal(t, 6, parts)
}

// blockingModel blocks until the context is done and then returns the
// context error, the way a hung provider request would.
type blockingModel struct {
	fakeLanguageModel
}

func (b *blockingModel) Generate(ctx context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRequestTimeoutModel_GenerateReportsTimeout(t *testing.T) {
	t.Parallel()

	m := newRequestTimeoutModel(&blockingModel{}, 10*time.Millisecond)

	_, err := m.Generate(t.Context(), fantasy.Call{})
	require.Error(t, err)

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, 10*time.Millisecond, timeoutErr.timeout)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the deadline must stay detectable through the chain")
	require.Contains(t, err.Error(), "timed out after 10ms")
}

func TestRequestTimeoutModel_StreamReportsTimeout(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	// A provider stream that fails with the context error once the deadline
	// fires, mirroring how SDKs surface mid-stream aborts.
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		<-inner.streamCtx.Done()
		yield(fantasy.StreamPart{Error: inner.streamCtx.Err()})
	}
	m := newRequestTimeoutModel(inner, 10*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var got error
	for part := range stream {
		if part.Error != nil {
			got = part.Error
		}
	}

	var timeoutErr *requestTimeoutError
	require.ErrorAs(t, got, &timeoutErr)
	require.ErrorIs(t, got, context.DeadlineExceeded)
}

func TestRequestTimeoutModel_ParentCancelPassesThrough(t *testing.T) {
	t.Parallel()

	m := newRequestTimeoutModel(&blockingModel{}, 5*time.Minute)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := m.Generate(ctx, fantasy.Call{})
	require.ErrorIs(t, err, context.Canceled)

	var timeoutErr *requestTimeoutError
	require.NotErrorAs(t, err, &timeoutErr, "user cancellation must not be reported as a timeout")
}

func TestRequestTimeoutErrorMessages(t *testing.T) {
	t.Parallel()

	err := &requestTimeoutError{timeout: time.Second}
	require.Equal(t, "LLM request timed out after 1s", err.Error())
	require.Contains(t, err.userMessage(), "1s")
	require.Contains(t, err.userMessage(), "options.request_timeout")

	err.detail = "context deadline exceeded"
	require.Equal(t, "LLM request timed out after 1s: context deadline exceeded", err.Error())

	idle := &requestTimeoutError{timeout: 2 * time.Second, idle: true}
	require.Equal(t, "LLM stream received no data for 2s", idle.Error())
	require.Contains(t, idle.userMessage(), "stopped sending data for 2s")
	require.Contains(t, idle.userMessage(), "options.request_timeout")
}

// timeoutOnlyModel streams a single error part shaped exactly like the one
// requestTimeoutModel produces when its deadline fires.
type timeoutOnlyModel struct {
	fakeLanguageModel
}

func (m *timeoutOnlyModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	timeoutErr := &requestTimeoutError{timeout: time.Second, idle: true}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: timeoutErr})
	}, nil
}

// TestRequestTimeoutRunFinishMessage pins what the user sees when a request
// exhausts its timeout: a "Request timed out" finish that names the budget
// and how to change it, instead of a provider error.
func TestRequestTimeoutRunFinishMessage(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &timeoutOnlyModel{}
	agent := testSessionAgent(env, model, model, "system")

	session, err := env.sessions.Create(t.Context(), "timeout session", t.TempDir())
	require.NoError(t, err)

	_, err = agent.Run(t.Context(), SessionAgentCall{
		Prompt:          "Hello",
		SessionID:       session.ID,
		MaxOutputTokens: 10000,
		NonInteractive:  true,
	})
	require.Error(t, err)

	requireTimeoutFinish(t, env, session.ID, "stopped sending data for 1s")
}

func requireTimeoutFinish(t *testing.T, env fakeEnv, sessionID, detail string) {
	t.Helper()
	msgs, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)

	var finish *message.Finish
	for _, msg := range msgs {
		if msg.Role != message.Assistant {
			continue
		}
		if part := msg.FinishPart(); part != nil {
			finish = part
		}
	}
	require.NotNil(t, finish, "the assistant message should carry a finish part")
	require.Equal(t, message.FinishReasonError, finish.Reason)
	require.Equal(t, "Request timed out", finish.Message)
	require.Contains(t, finish.Details, detail)
	require.Contains(t, finish.Details, "options.request_timeout")
}

// hangingStreamModel connects, then sends nothing until its context ends,
// and counts how often it was asked to stream.
type hangingStreamModel struct {
	fakeLanguageModel
	calls atomic.Int32
	// sdkErr is what the SDK reports when the request context is cancelled.
	sdkErr func(ctx context.Context) error
}

func (m *hangingStreamModel) Stream(ctx context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	m.calls.Add(1)
	return func(yield func(fantasy.StreamPart) bool) {
		<-ctx.Done()
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: m.sdkErr(ctx)})
	}, nil
}

// A timed-out request must not be retried, even when the SDK reports the
// cancelled HTTP/2 stream as a retryable transport error: otherwise a hung
// provider takes MaxRetries+1 full timeouts to fail.
func TestRequestTimeoutIsNotRetried(t *testing.T) {
	t.Parallel()

	for name, sdkErr := range map[string]func(ctx context.Context) error{
		"context error": func(ctx context.Context) error { return ctx.Err() },
		"http2 stream cancel as provider error": func(context.Context) error {
			return fantasy.NewTransportError(errors.New("stream error: stream ID 3; CANCEL"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inner := &hangingStreamModel{sdkErr: sdkErr}
			model := newRequestTimeoutModel(inner, 20*time.Millisecond)
			agent := fantasy.NewAgent(model, fantasy.WithMaxRetries(3))

			_, err := agent.Stream(t.Context(), fantasy.AgentStreamCall{Prompt: "go"})

			var timeoutErr *requestTimeoutError
			require.ErrorAs(t, err, &timeoutErr)
			require.Equal(t, int32(1), inner.calls.Load(), "a timed-out request must not be retried")
			var providerErr *fantasy.ProviderError
			require.NotErrorAs(t, err, &providerErr, "the SDK error must not leak into the chain")
		})
	}
}

// Anthropic keepalives arrive as stream parts during long thinking pauses,
// so each one must reset the idle clock.
func TestRequestTimeoutKeepalivesResetTheClock(t *testing.T) {
	t.Parallel()

	inner := &fakeLanguageModel{}
	inner.stream = func(yield func(fantasy.StreamPart) bool) {
		for range 6 {
			time.Sleep(30 * time.Millisecond)
			if inner.streamCtx.Err() != nil {
				yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: inner.streamCtx.Err()})
				return
			}
			if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeKeepalive}) {
				return
			}
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}
	m := newRequestTimeoutModel(inner, 80*time.Millisecond)
	stream, err := m.Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)

	var parts int
	for part := range stream {
		require.NoError(t, part.Error, "180ms of keepalives must not trip an 80ms idle timeout")
		parts++
	}
	require.Equal(t, 7, parts)
}

func TestRequestTimeoutMessageOmitsOwnCancellation(t *testing.T) {
	t.Parallel()

	inner := &hangingStreamModel{sdkErr: func(ctx context.Context) error {
		return fmt.Errorf("Post \"http://x/v1/chat\": %w", ctx.Err())
	}}
	stream, err := newRequestTimeoutModel(inner, 10*time.Millisecond).Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)
	var got error
	for part := range stream {
		got = part.Error
	}
	require.Equal(t, "LLM stream received no data for 10ms", got.Error())

	other := &hangingStreamModel{sdkErr: func(context.Context) error { return errors.New("connection reset by peer") }}
	stream, err = newRequestTimeoutModel(other, 10*time.Millisecond).Stream(t.Context(), fantasy.Call{})
	require.NoError(t, err)
	for part := range stream {
		got = part.Error
	}
	require.Equal(t, "LLM stream received no data for 10ms: connection reset by peer", got.Error())
}

func TestRequestTimeoutPreservesModelIdentity(t *testing.T) {
	t.Parallel()

	m := newRequestTimeoutModel(&fakeLanguageModel{}, time.Second)
	require.Equal(t, "fake", m.Provider())
	require.Equal(t, "fake-model", m.Model())
}

// The wrapper sits under the whole session agent: a hung stream ends the
// run with the timeout finish instead of blocking it.
func TestRequestTimeoutEndsAHungRun(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	inner := &hangingStreamModel{sdkErr: func(ctx context.Context) error { return ctx.Err() }}
	model := newRequestTimeoutModel(inner, 50*time.Millisecond)
	agent := testSessionAgent(env, model, model, "system")
	session, err := env.sessions.Create(t.Context(), "hung session", t.TempDir())
	require.NoError(t, err)

	start := time.Now()
	_, err = agent.Run(t.Context(), SessionAgentCall{
		Prompt:          "Hello",
		SessionID:       session.ID,
		MaxOutputTokens: 1000,
		NonInteractive:  true,
	})
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second)
	require.Equal(t, int32(1), inner.calls.Load())

	requireTimeoutFinish(t, env, session.ID, "stopped sending data for 50ms")
}

func newTimeoutTestCoordinator(t *testing.T, requestTimeout *int) *coordinator {
	t.Helper()
	cfg, err := config.Init(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("openai", config.ProviderConfig{
		ID: "openai", Type: catwalk.TypeOpenAI, APIKey: "unused", BaseURL: "http://127.0.0.1:1",
		Models: []catwalk.Model{
			{ID: "gpt-4o", ContextWindow: 128000, DefaultMaxTokens: 1024},
			{ID: "gpt-4o-mini", ContextWindow: 128000, DefaultMaxTokens: 1024},
		},
	})
	cfg.Config().Providers = providers
	cfg.Config().Models = map[config.SelectedModelType]config.SelectedModel{
		config.SelectedModelTypeLarge: {Provider: "openai", Model: "gpt-4o"},
		config.SelectedModelTypeSmall: {Provider: "openai", Model: "gpt-4o-mini"},
	}
	cfg.Config().Options.RequestTimeout = requestTimeout
	return &coordinator{cfg: cfg}
}

// Every agent's large and small models are built in buildAgentModels, so
// that is where the timeout must be applied: the orchestrator, and an agent
// with its own model override.
func TestBuildAgentModelsAppliesRequestTimeout(t *testing.T) {
	t.Parallel()

	seconds := func(n int) *int { return &n }
	for name, tc := range map[string]struct {
		setting *int
		want    time.Duration
	}{
		"default":  {nil, config.DefaultRequestTimeout},
		"custom":   {seconds(300), 5 * time.Minute},
		"disabled": {seconds(0), 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newTimeoutTestCoordinator(t, tc.setting)
			for _, agentCfg := range []config.Agent{
				{ID: config.AgentOrchestrator},
				{ID: "specialist", Model: "openai/gpt-4o-mini"},
			} {
				large, small, err := c.buildAgentModels(t.Context(), agentCfg)
				require.NoError(t, err)
				for kind, m := range map[string]fantasy.LanguageModel{"large": large.Model, "small": small.Model} {
					wrapped, ok := m.(requestTimeoutModel)
					if tc.want == 0 {
						require.False(t, ok, "%s %s: disabled timeout must not wrap", agentCfg.ID, kind)
						continue
					}
					require.True(t, ok, "%s %s model is not wrapped", agentCfg.ID, kind)
					require.Equal(t, tc.want, wrapped.timeout)
				}
			}
		})
	}
}

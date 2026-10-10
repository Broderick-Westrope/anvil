package agent

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/openrouter"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/stretchr/testify/require"
)

// sideCallAgent builds a session agent whose large and small models are
// large and small, with the given provider prefix and config.
func sideCallAgent(env fakeEnv, large, small Model, prefix string, providerCfg config.ProviderConfig) *sessionAgent {
	return NewSessionAgent(SessionAgentOptions{
		LargeModel:         large,
		SmallModel:         small,
		SystemPromptPrefix: prefix,
		ProviderConfig:     providerCfg,
		SystemPrompt:       "system",
		IsYolo:             true,
		Sessions:           env.sessions,
		Messages:           env.messages,
		UsageRecorder:      env.usage,
		AgentName:          "orchestrator",
		WorkingDir:         env.workingDir,
	}).(*sessionAgent)
}

// sessionWithUserMessage creates a session whose branch holds one user
// message.
func sessionWithUserMessage(t *testing.T, env fakeEnv) (sessionID, leafID string) {
	t.Helper()
	sess := newTestSession(t, env)
	msg, err := env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "Help me name this"}},
	})
	require.NoError(t, err)
	return sess.ID, msg.ID
}

func systemTexts(msgs []fantasy.Message) []string {
	var texts []string
	for _, msg := range msgs {
		if msg.Role == fantasy.MessageRoleSystem {
			texts = append(texts, textOf(msg))
		}
	}
	return texts
}

// TestSideCallsFrameRequests pins how summaries, titles and small-model
// completions frame their requests: the provider's system prompt prefix
// first, then the Anthropic OAuth billing and identity messages. It sets
// the OAuth system mode, so it must not run in parallel.
func TestSideCallsFrameRequests(t *testing.T) {
	t.Setenv(SystemModeEnvVar, SystemModeA)
	calls := map[string]struct {
		system string
		run    func(t *testing.T, env fakeEnv, a *sessionAgent)
	}{
		"summary": {
			system: string(summaryPrompt),
			run: func(t *testing.T, env fakeEnv, a *sessionAgent) {
				sessionID, _ := sessionWithUserMessage(t, env)
				require.NoError(t, a.Summarize(t.Context(), sessionID, nil))
			},
		},
		"title": {
			system: string(titlePrompt) + "\n /no_think",
			run: func(t *testing.T, env fakeEnv, a *sessionAgent) {
				sessionID, _ := sessionWithUserMessage(t, env)
				a.generateTitle(t.Context(), sessionID, []message.Message{{
					Role:  message.User,
					Parts: []message.ContentPart{message.TextContent{Text: "Help me name this"}},
				}})
			},
		},
		"small": {
			system: "review",
			run: func(t *testing.T, _ fakeEnv, a *sessionAgent) {
				_, _, err := a.completeSmall(t.Context(), "review", "is this safe?")
				require.NoError(t, err)
			},
		},
	}
	oauthCfg := config.ProviderConfig{Type: catwalk.TypeAnthropic, OAuthToken: &oauth.Token{AccessToken: "token"}}
	for name, call := range calls {
		for _, prefix := range []string{"", "Provider prefix."} {
			for _, withOAuth := range []bool{false, true} {
				t.Run(name+"/prefix="+prefix+"/oauth="+map[bool]string{false: "no", true: "yes"}[withOAuth], func(t *testing.T) {
					env := testEnv(t)
					model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
						return textReply("Reply", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1, OutputTokens: 1}), nil
					}}
					m := Model{Model: model, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
					var providerCfg config.ProviderConfig
					if withOAuth {
						providerCfg = oauthCfg
					}
					a := sideCallAgent(env, m, m, prefix, providerCfg)
					call.run(t, env, a)

					recorded := model.recorded()
					require.Len(t, recorded, 1)
					got := systemTexts(recorded[0].Prompt)
					var want []string
					if prefix != "" {
						want = append(want, prefix)
					}
					want = append(want, call.system)
					if withOAuth {
						require.Len(t, got, len(want)+2)
						require.Contains(t, got[0], "x-anthropic-billing-header:")
						require.Equal(t, AnthropicIdentityPrefix, got[1])
						got = got[2:]
					}
					require.Equal(t, want, got)
				})
			}
		}
	}
}

func TestSummarizeFailure(t *testing.T) {
	t.Parallel()
	streamed := []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeReasoningStart, ID: "r"},
		{Type: fantasy.StreamPartTypeReasoningDelta, ID: "r", Delta: "Thinking."},
		{Type: fantasy.StreamPartTypeReasoningEnd, ID: "r", ProviderMetadata: fantasy.ProviderMetadata{
			anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: "sig"},
		}},
		{Type: fantasy.StreamPartTypeTextStart, ID: "t"},
		{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: "Partial summary."},
		{Type: fantasy.StreamPartTypeTextEnd, ID: "t"},
	}
	badRequest := &fantasy.ProviderError{Title: "bad request", Message: "nope", StatusCode: http.StatusBadRequest}
	unauthorized := &fantasy.ProviderError{Title: "unauthorized", Message: "nope", StatusCode: http.StatusUnauthorized}
	otherErr := errors.New("stream broke")
	tests := map[string]struct {
		// fail returns the error that ends the stream after the streamed
		// parts. cancel cancels the summarize context.
		fail     func(cancel context.CancelFunc) error
		wantErr  error
		abandons bool
	}{
		"provider error keeps the placeholder": {
			fail:    func(context.CancelFunc) error { return badRequest },
			wantErr: badRequest,
		},
		"unauthorized drops the placeholder": {
			fail:     func(context.CancelFunc) error { return unauthorized },
			wantErr:  unauthorized,
			abandons: true,
		},
		"canceled error drops the placeholder": {
			fail:     func(context.CancelFunc) error { return context.Canceled },
			wantErr:  context.Canceled,
			abandons: true,
		},
		"canceled context drops the placeholder": {
			fail: func(cancel context.CancelFunc) error {
				cancel()
				return otherErr
			},
			abandons: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				return append(slices.Clone(streamed), fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: tt.fail(cancel)}), nil
			}}
			m := Model{Model: model, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
			a := sideCallAgent(env, m, m, "", config.ProviderConfig{})
			sessionID, leafID := sessionWithUserMessage(t, env)

			err := a.summarizeOwned(ctx, sessionID, nil)
			require.Error(t, err)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}

			sess, getErr := env.sessions.Get(t.Context(), sessionID)
			require.NoError(t, getErr)
			msgs, listErr := env.messages.List(t.Context(), sessionID)
			require.NoError(t, listErr)
			idx := slices.IndexFunc(msgs, func(m message.Message) bool { return m.MessageType == message.MessageTypeCompaction })
			if tt.abandons {
				require.Equal(t, -1, idx)
				require.Equal(t, leafID, sess.LeafMessageID)
				return
			}
			require.GreaterOrEqual(t, idx, 0)
			placeholder := msgs[idx]
			require.Equal(t, placeholder.ID, sess.LeafMessageID)
			require.Equal(t, "Thinking.", placeholder.ReasoningContent().Thinking)
			require.Equal(t, "sig", placeholder.ReasoningContent().Signature)
			require.Equal(t, "Partial summary.", placeholder.Content().Text)
			finish := placeholder.FinishPart()
			require.NotNil(t, finish)
			require.Equal(t, message.FinishReasonError, finish.Reason)
			require.Equal(t, "Summarization Error", finish.Message)
			require.Equal(t, tt.wantErr.Error(), finish.Details)
		})
	}
}

func TestSummarizeChargesSessionCost(t *testing.T) {
	t.Parallel()
	usage := fantasy.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	tests := map[string]struct {
		openrouterCost *float64
		flatRate       bool
		want           float64
	}{
		"catwalk prices":     {want: 3},
		"openrouter cost":    {openrouterCost: new(0.25), want: 0.25},
		"flat rate":          {flatRate: true},
		"flat rate override": {openrouterCost: new(0.25), flatRate: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				parts := textReply("Summary.", fantasy.FinishReasonStop, usage)
				if tt.openrouterCost != nil {
					parts[len(parts)-1].ProviderMetadata = fantasy.ProviderMetadata{
						openrouter.Name: &openrouter.ProviderMetadata{Usage: openrouter.UsageAccounting{Cost: *tt.openrouterCost}},
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

			sess, err := env.sessions.Get(t.Context(), sessionID)
			require.NoError(t, err)
			require.InDelta(t, tt.want, sess.Cost, 1e-9)
		})
	}
}

func TestGenerateTitle(t *testing.T) {
	t.Parallel()
	conversation := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "Help me name this"}},
	}}
	usage := fantasy.Usage{InputTokens: 1_000_000, CacheCreationTokens: 1_000_000, CacheReadTokens: 1_000_000, OutputTokens: 1_000_000}
	tests := map[string]struct {
		canReason      bool
		fail           bool
		openrouterCost *float64
		flatRate       bool
		wantTitle      string
		wantMaxTokens  int64
		wantCost       float64
	}{
		"catwalk prices": {wantTitle: "Named", wantMaxTokens: 40, wantCost: 1 + 2 + 3 + 4},
		"reasoning model": {
			canReason: true, wantTitle: "Named", wantMaxTokens: 777, wantCost: 1 + 2 + 3 + 4,
		},
		"openrouter cost": {openrouterCost: new(0.5), wantTitle: "Named", wantMaxTokens: 40, wantCost: 0.5},
		"flat rate":       {openrouterCost: new(0.5), flatRate: true, wantTitle: "Named", wantMaxTokens: 40},
		"every model fails": {
			fail: true, wantTitle: DefaultSessionName, wantMaxTokens: 40,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				if tt.fail {
					return nil, errors.New("no title for you")
				}
				parts := textReply("Named\n", fantasy.FinishReasonStop, usage)
				if tt.openrouterCost != nil {
					parts[len(parts)-1].ProviderMetadata = fantasy.ProviderMetadata{
						openrouter.Name: &openrouter.ProviderMetadata{Usage: openrouter.UsageAccounting{Cost: *tt.openrouterCost}},
					}
				}
				return parts, nil
			}}
			m := Model{
				Model: model,
				CatwalkCfg: catwalk.Model{
					ContextWindow: 200000, DefaultMaxTokens: 777, CanReason: tt.canReason,
					CostPer1MInCached: 1, CostPer1MOutCached: 2, CostPer1MIn: 3, CostPer1MOut: 4,
				},
				FlatRate: tt.flatRate,
			}
			a := sideCallAgent(env, m, m, "", config.ProviderConfig{})
			sess := newTestSession(t, env)

			a.generateTitle(t.Context(), sess.ID, conversation)

			got, err := env.sessions.Get(t.Context(), sess.ID)
			require.NoError(t, err)
			require.Equal(t, tt.wantTitle, got.Title)
			recorded := model.recorded()
			require.NotEmpty(t, recorded)
			require.NotNil(t, recorded[0].MaxOutputTokens)
			require.Equal(t, tt.wantMaxTokens, *recorded[0].MaxOutputTokens)
			if tt.fail {
				require.Len(t, recorded, 2, "the large model is tried after the small one")
				return
			}
			require.InDelta(t, tt.wantCost, got.Cost, 1e-9)
			require.Equal(t, usage.InputTokens+usage.CacheCreationTokens, got.PromptTokens)
			require.Equal(t, usage.OutputTokens, got.CompletionTokens)
		})
	}
}

func TestCompleteSmall(t *testing.T) {
	t.Parallel()
	t.Run("no small model", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t)
		a := sideCallAgent(env, Model{}, Model{}, "", config.ProviderConfig{})
		_, _, err := a.completeSmall(t.Context(), "review", "is this safe?")
		require.EqualError(t, err, "small model not configured")
	})
	for name, tt := range map[string]struct {
		canReason bool
		maxTokens int64
		want      int64
	}{
		"plain model":               {maxTokens: 4000, want: completeSmallMaxTokens},
		"reasoning model with more": {canReason: true, maxTokens: 4000, want: 4000},
		"reasoning model with less": {canReason: true, maxTokens: 10, want: completeSmallMaxTokens},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				return textReply("allow", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1}), nil
			}}
			m := Model{Model: model, CatwalkCfg: catwalk.Model{DefaultMaxTokens: tt.maxTokens, CanReason: tt.canReason}, ModelCfg: config.SelectedModel{Model: "small-id"}}
			a := sideCallAgent(env, m, m, "", config.ProviderConfig{})
			reply, modelID, err := a.completeSmall(t.Context(), "review", "is this safe?")
			require.NoError(t, err)
			require.Equal(t, "allow", reply)
			require.Equal(t, "small-id", modelID)
			recorded := model.recorded()
			require.Len(t, recorded, 1)
			require.Equal(t, tt.want, *recorded[0].MaxOutputTokens)
		})
	}
	t.Run("stream error", func(t *testing.T) {
		t.Parallel()
		env := testEnv(t)
		model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
			return nil, errors.New("down")
		}}
		m := Model{Model: model, ModelCfg: config.SelectedModel{Model: "small-id"}}
		a := sideCallAgent(env, m, m, "", config.ProviderConfig{})
		_, modelID, err := a.completeSmall(t.Context(), "review", "is this safe?")
		require.ErrorContains(t, err, "small model: ")
		require.True(t, strings.Contains(err.Error(), "down"))
		require.Equal(t, "small-id", modelID)
	})
}

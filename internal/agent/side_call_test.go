package agent

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
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
// message, and returns the session's ID and the message's ID.
func sessionWithUserMessage(t *testing.T, env fakeEnv) (string, string) {
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

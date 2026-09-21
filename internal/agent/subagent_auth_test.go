package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

func TestRunSubAgentRefreshesCredentials(t *testing.T) {
	for _, test := range []struct {
		name      string
		expiresIn time.Duration
		wantAuth  []string
		think     bool
		reject    bool
		fresh     bool
	}{
		{name: "expired token is refreshed before requesting", expiresIn: -time.Hour, wantAuth: []string{"Bearer fresh-token"}},
		{name: "revoked token is refreshed on retry", expiresIn: time.Hour, wantAuth: []string{"Bearer stale-token", "Bearer fresh-token"}},
		{name: "thinking survives proactive refresh", expiresIn: -time.Hour, wantAuth: []string{"Bearer fresh-token"}, think: true},
		{name: "thinking survives auth retry", expiresIn: time.Hour, wantAuth: []string{"Bearer stale-token", "Bearer fresh-token"}, think: true},
		{name: "rejected refreshed token stops retrying", expiresIn: time.Hour, wantAuth: []string{"Bearer stale-token", "Bearer fresh-token"}, reject: true},
		{name: "valid token does not rebuild model", expiresIn: time.Hour, wantAuth: []string{"Bearer fresh-token"}, fresh: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
			t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
			t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
			workingDir := t.TempDir()
			cfg, err := config.Init(workingDir, t.TempDir(), false)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(config.GlobalConfigData(), []byte(`{"providers":{"anthropic":{"oauth":{"access_token":"fresh-token","refresh_token":"fresh-refresh","expires_in":3600,"expires_at":9999999999}}}}`), 0o600))

			var mu sync.Mutex
			var authorizations []string
			var betaHeaders []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				auth := request.Header.Get("Authorization")
				mu.Lock()
				authorizations = append(authorizations, auth)
				betaHeaders = append(betaHeaders, request.Header.Get("Anthropic-Beta"))
				mu.Unlock()
				if auth != "Bearer fresh-token" || test.reject {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid OAuth token"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []struct{ name, data string }{
					{"message_start", `{"type":"message_start","message":{"id":"msg_auth","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`},
					{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
					{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Authenticated successfully."}}`},
					{"content_block_stop", `{"type":"content_block_stop","index":0}`},
					{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`},
					{"message_stop", `{"type":"message_stop"}`},
				} {
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data)
				}
			}))
			t.Cleanup(server.Close)

			providers := csync.NewMap[string, config.ProviderConfig]()
			providers.Set("openai", config.ProviderConfig{
				ID: "openai", Type: catwalk.TypeOpenAI, APIKey: "unused", BaseURL: server.URL,
				Models: []catwalk.Model{{ID: "gpt-4o", ContextWindow: 128000, DefaultMaxTokens: 1024}},
			})
			providerCfg := config.ProviderConfig{
				ID: "anthropic", Type: catwalk.TypeAnthropic, APIKey: "Bearer stale-token", BaseURL: server.URL,
				OAuthToken: &oauth.Token{AccessToken: "stale-token", RefreshToken: "stale-refresh", ExpiresAt: time.Now().Add(test.expiresIn).Unix()},
				Models:     []catwalk.Model{{ID: "claude-haiku-4-5", ContextWindow: 200000, DefaultMaxTokens: 1024}},
			}
			if test.fresh {
				providerCfg.APIKey = "Bearer fresh-token"
				providerCfg.OAuthToken.AccessToken = "fresh-token"
			}
			providers.Set("anthropic", providerCfg)
			cfg.Config().Providers = providers
			cfg.Config().Models = map[config.SelectedModelType]config.SelectedModel{
				config.SelectedModelTypeLarge: {Provider: "openai", Model: "gpt-4o"},
				config.SelectedModelTypeSmall: {Provider: "anthropic", Model: "claude-haiku-4-5"},
			}
			conn, err := db.Connect(t.Context(), t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			queries := db.New(conn)
			sessions := session.NewService(queries, conn)
			messages := message.NewService(queries, message.WithConn(conn))
			coord := &coordinator{
				cfg: cfg, sessions: sessions, messages: messages,
				permissions:  permission.NewPermissionService(workingDir, config.YoloStandard, nil, nil),
				agents:       csync.NewMap[string, SessionAgent](),
				agentConfigs: map[string]config.Agent{config.AgentOrchestrator: {ID: config.AgentOrchestrator}},
			}
			large, small, err := coord.buildAgentModels(t.Context(), config.Agent{ID: config.AgentOrchestrator})
			require.NoError(t, err)
			coord.orchestrator = NewSessionAgent(SessionAgentOptions{LargeModel: large, SmallModel: small})
			specialist, _, err := coord.buildAgentModels(t.Context(), config.Agent{
				ID: "specialist", Model: "anthropic/claude-haiku-4-5", Think: &test.think,
			})
			require.NoError(t, err)
			specialist.Model = &struct{ fantasy.LanguageModel }{specialist.Model}
			agent := NewSessionAgent(SessionAgentOptions{
				LargeModel: specialist, SmallModel: small, ProviderConfig: providerCfg,
				SystemPrompt: "Respond briefly.", Sessions: sessions, Messages: messages,
				DisableAutoSummarize: true, IsSubAgent: true,
			})
			coord.agents.Set("specialist|2|anthropic/claude-haiku-4-5", agent)
			parent, err := sessions.Create(t.Context(), "Parent", workingDir)
			require.NoError(t, err)
			parentMessage, err := messages.Create(t.Context(), parent.ID, message.CreateMessageParams{Role: message.Assistant})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			response, err := coord.runSubAgent(ctx, subAgentParams{
				Agent: agent, SessionID: parent.ID, AgentMessageID: parentMessage.ID,
				ToolCallID: "auth_task", Prompt: "Check authentication.", SessionTitle: "Authentication",
			})
			require.NoError(t, err)
			require.Equal(t, test.reject, response.IsError, "%s", response.Content)
			if test.reject {
				require.Contains(t, response.Content, "invalid OAuth token")
			} else {
				require.Equal(t, "Authenticated successfully.", response.Content)
			}
			if test.fresh {
				require.Same(t, specialist.Model, agent.Model().Model)
			}
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, test.wantAuth, authorizations)
			if test.think {
				for _, header := range betaHeaders {
					require.Contains(t, header, "interleaved-thinking-2025-05-14")
				}
			}
			require.Equal(t, specialist.ModelCfg, agent.Model().ModelCfg)
			require.Equal(t, "anthropic", agent.Model().ModelCfg.Provider)
			require.Equal(t, "claude-haiku-4-5", agent.Model().ModelCfg.Model)
			childID := sessions.CreateAgentToolSessionID(parentMessage.ID, "auth_task")
			history, err := messages.List(ctx, childID)
			require.NoError(t, err)
			require.Len(t, history, 2)
		})
	}
}

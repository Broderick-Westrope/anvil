package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	anthropicoauth "github.com/Broderick-Westrope/anvil/internal/oauth/anthropic"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAgenticFetchTestCoordinator(t *testing.T, cfg *config.Config) (*coordinator, context.Context) {
	t.Helper()
	workingDir := t.TempDir()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q, message.WithConn(conn))
	cfg.Options = &config.Options{ProjectDirectory: workingDir, DisableAutoSummarize: true}
	c := &coordinator{
		cfg: config.NewTestStore(cfg), sessions: sessions, messages: messages,
		permissions: permission.NewPermissionService(workingDir, config.YoloStandard, nil, nil),
	}
	parent, err := sessions.Create(t.Context(), "Fetch parent", workingDir)
	require.NoError(t, err)
	parentMessage, err := messages.Create(t.Context(), parent.ID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, parent.ID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, parentMessage.ID)
	return c, ctx
}

func TestAgenticFetchModelSelection(t *testing.T) {
	const smallPrefix = "Small provider prefix sentinel."
	const fetchPrefix = "Fetch provider prefix sentinel."
	const finalOutput = "Fetch model selection complete."
	for _, tt := range []struct {
		name            string
		model           string
		wantModel       string
		wantPrefix      string
		forbiddenPrefix string
		fallback        bool
		disabled        bool
		oauthMode       string
	}{
		{name: "unset", wantModel: "claude-haiku-4-5", wantPrefix: smallPrefix, forbiddenPrefix: fetchPrefix},
		{name: "override", model: "fetch-anthropic/claude-sonnet-4-5", wantModel: "claude-sonnet-4-5", wantPrefix: fetchPrefix, forbiddenPrefix: smallPrefix},
		{name: "unknown model", model: "fetch-anthropic/does-not-exist", wantModel: "claude-haiku-4-5", wantPrefix: smallPrefix, forbiddenPrefix: fetchPrefix, fallback: true},
		{name: "bad format", model: "no-slash", wantModel: "claude-haiku-4-5", wantPrefix: smallPrefix, forbiddenPrefix: fetchPrefix, fallback: true},
		{name: "unknown provider", model: "missing/claude-sonnet-4-5", wantModel: "claude-haiku-4-5", wantPrefix: smallPrefix, forbiddenPrefix: fetchPrefix, fallback: true},
		{name: "disabled provider", model: "fetch-anthropic/claude-sonnet-4-5", wantModel: "claude-haiku-4-5", wantPrefix: smallPrefix, forbiddenPrefix: fetchPrefix, fallback: true, disabled: true},
		{name: "OAuth mode A", model: "fetch-anthropic/claude-sonnet-4-5", wantModel: "claude-sonnet-4-5", wantPrefix: fetchPrefix, forbiddenPrefix: smallPrefix, oauthMode: SystemModeA},
		{name: "OAuth mode B", model: "fetch-anthropic/claude-sonnet-4-5", wantModel: "claude-sonnet-4-5", wantPrefix: fetchPrefix, forbiddenPrefix: smallPrefix, oauthMode: SystemModeB},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_API_KEY", "dummy-env-key")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
			t.Setenv(SystemModeEnvVar, tt.oauthMode)
			var logs bytes.Buffer
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(logger) })
			type block struct {
				Text string `json:"text"`
			}
			type request struct {
				Model    string  `json:"model"`
				System   []block `json:"system"`
				Messages []struct {
					Role    string  `json:"role"`
					Content []block `json:"content"`
				} `json:"messages"`
			}
			requests := make(chan request, 1)
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
					t.Errorf("Unexpected provider request: %s %s", r.Method, r.URL)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				var body request
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("Decode provider request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				if count.Add(1) > 1 {
					t.Error("Unexpected extra provider request")
					http.Error(w, "extra request", http.StatusBadRequest)
					return
				}
				requests <- body
				if tt.oauthMode != "" && r.Header.Get("Authorization") != "Bearer dummy-oauth-token" {
					t.Errorf("Unexpected authorization: %q", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event, data string) {
					if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
						t.Errorf("Write provider event: %v", err)
					}
				}
				emit("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_fetch","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`, body.Model))
				emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
				emit("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, finalOutput))
				emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
				emit("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`)
				emit("message_stop", `{"type":"message_stop"}`)
			}))
			t.Cleanup(server.Close)
			providers := csync.NewMap[string, config.ProviderConfig]()
			providers.Set("small-anthropic", config.ProviderConfig{
				ID: "small-anthropic", Type: catwalk.TypeAnthropic, APIKey: "dummy-api-key", BaseURL: server.URL,
				Models:             []catwalk.Model{{ID: "claude-haiku-4-5", ContextWindow: 200000, DefaultMaxTokens: 1024}},
				SystemPromptPrefix: smallPrefix,
			})
			fetchProvider := config.ProviderConfig{
				ID: "fetch-anthropic", Type: catwalk.TypeAnthropic, APIKey: "dummy-api-key", BaseURL: server.URL,
				Models:             []catwalk.Model{{ID: "claude-sonnet-4-5", ContextWindow: 200000, DefaultMaxTokens: 1024}},
				SystemPromptPrefix: fetchPrefix, Disable: tt.disabled,
			}
			if tt.oauthMode != "" {
				fetchProvider.APIKey = "Bearer dummy-oauth-token"
				fetchProvider.OAuthToken = &oauth.Token{AccessToken: "dummy-oauth-token", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			}
			providers.Set(fetchProvider.ID, fetchProvider)
			c, ctx := newAgenticFetchTestCoordinator(t, &config.Config{
				Providers: providers,
				Models: map[config.SelectedModelType]config.SelectedModel{
					config.SelectedModelTypeLarge: {Provider: "small-anthropic", Model: "claude-haiku-4-5"},
					config.SelectedModelTypeSmall: {Provider: "small-anthropic", Model: "claude-haiku-4-5"},
				},
				Tools: config.Tools{AgenticFetch: config.ToolAgenticFetch{Model: tt.model}},
			})
			tool, err := c.agenticFetchTool(ctx, server.Client())
			require.NoError(t, err)
			result, err := tool.Run(ctx, fantasy.ToolCall{ID: "fetch_call", Name: tools.AgenticFetchToolName, Input: `{"prompt":"Find the model selection sentinel."}`})
			require.NoError(t, err)
			require.False(t, result.IsError, "%+v", result)
			require.Equal(t, finalOutput, result.Content)
			require.EqualValues(t, 1, count.Load())
			body := <-requests
			require.Equal(t, tt.wantModel, body.Model)
			var systemTexts, userTexts []string
			for _, part := range body.System {
				systemTexts = append(systemTexts, part.Text)
			}
			for _, msg := range body.Messages {
				if msg.Role == "user" {
					for _, part := range msg.Content {
						userTexts = append(userTexts, part.Text)
					}
				}
			}
			systemText := strings.Join(systemTexts, "\n")
			userText := strings.Join(userTexts, "\n")
			require.Equal(t, 1, strings.Count(systemText+userText, tt.wantPrefix))
			require.NotContains(t, systemText+userText, tt.forbiddenPrefix)
			if tt.oauthMode == SystemModeB {
				require.Contains(t, userText, tt.wantPrefix)
				require.NotContains(t, systemText, tt.wantPrefix)
			} else {
				require.Equal(t, 1, strings.Count(systemText, tt.wantPrefix))
			}
			if tt.fallback {
				require.Contains(t, logs.String(), "Failed to resolve agentic_fetch model")
			} else {
				require.NotContains(t, logs.String(), "Failed to resolve agentic_fetch model")
			}
		})
	}
}

func TestRefreshSubAgentModelsKeepsAgenticFetchModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "dummy-env-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("anthropic", config.ProviderConfig{
		ID: "anthropic", Type: catwalk.TypeAnthropic, APIKey: "dummy-api-key",
		Models: []catwalk.Model{
			{ID: "claude-haiku-4-5", ContextWindow: 200000, DefaultMaxTokens: 1024},
			{ID: "claude-sonnet-4-5", ContextWindow: 200000, DefaultMaxTokens: 1024},
		},
	})
	c := &coordinator{cfg: config.NewTestStore(&config.Config{
		Options:   &config.Options{},
		Providers: providers,
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeSmall: {Provider: "anthropic", Model: "claude-haiku-4-5"},
		},
		Tools: config.Tools{AgenticFetch: config.ToolAgenticFetch{Model: "anthropic/claude-sonnet-4-5"}},
	})}
	fetch, small, err := c.buildAgenticFetchModel(t.Context())
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5", fetch.ModelCfg.Model)
	require.Equal(t, "claude-haiku-4-5", small.ModelCfg.Model)
	agent := NewSessionAgent(SessionAgentOptions{LargeModel: fetch, SmallModel: small})
	require.NoError(t, c.refreshSubAgentModels(t.Context(), agent))
	require.Equal(t, "claude-sonnet-4-5", agent.Model().ModelCfg.Model)
	require.Equal(t, "claude-sonnet-4-5", agent.Model().Model.Model())
}

func TestAgenticFetchAnthropicOAuth(t *testing.T) {
	for _, mode := range []string{SystemModeA, SystemModeB} {
		for _, withOAuth := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/oauth=%t", mode, withOAuth), func(t *testing.T) {
				t.Setenv(SystemModeEnvVar, mode)
				t.Setenv("ANTHROPIC_API_KEY", "dummy-env-key")
				t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
				const prefix = "Fetch provider prefix sentinel."
				const userPrompt = "Find the fetch regression sentinel."
				const finalOutput = "Fetch regression complete."
				type block struct {
					Type      string          `json:"type"`
					Text      string          `json:"text"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
					IsError   bool            `json:"is_error"`
				}
				type request struct {
					Model    string  `json:"model"`
					Stream   bool    `json:"stream"`
					System   []block `json:"system"`
					Messages []struct {
						Role    string  `json:"role"`
						Content []block `json:"content"`
					} `json:"messages"`
				}
				requests := make(chan request, 2)
				var count atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
						t.Errorf("Unexpected provider request: %s %s", r.Method, r.URL)
						http.Error(w, "unexpected request", http.StatusBadRequest)
						return
					}
					var body request
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("Decode provider request: %v", err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					n := count.Add(1)
					if n > 2 {
						t.Error("Unexpected extra provider request")
						http.Error(w, "extra request", http.StatusBadRequest)
						return
					}
					requests <- body
					if withOAuth {
						assert.Equal(t, "Bearer dummy-oauth-token", r.Header.Get("Authorization"))
						assert.Empty(t, r.Header.Get("X-Api-Key"))
						assert.Equal(t, "true", r.URL.Query().Get("beta"))
					} else {
						assert.Equal(t, "dummy-api-key", r.Header.Get("X-Api-Key"))
						assert.Empty(t, r.Header.Get("Authorization"))
						assert.Empty(t, r.URL.Query().Get("beta"))
					}
					w.Header().Set("Content-Type", "text/event-stream")
					emit := func(event, data string) {
						_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
						assert.NoError(t, err)
					}
					emit("message_start", `{"type":"message_start","message":{"id":"msg_fetch","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
					if n == 1 {
						emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"glob_fetch","name":"glob","input":{}}}`)
						emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"*.md\"}"}}`)
					} else {
						emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
						emit("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, finalOutput))
					}
					emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
					stopReason := "end_turn"
					if n == 1 {
						stopReason = "tool_use"
					}
					emit("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":5}}`, stopReason))
					emit("message_stop", `{"type":"message_stop"}`)
				}))
				t.Cleanup(server.Close)

				workingDir := t.TempDir()
				conn, err := db.Connect(t.Context(), t.TempDir())
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, conn.Close()) })
				q := db.New(conn)
				sessions := session.NewService(q, conn)
				messages := message.NewService(q, message.WithConn(conn))
				providers := csync.NewMap[string, config.ProviderConfig]()
				providers.Set("large-openai", config.ProviderConfig{
					ID: "large-openai", Type: catwalk.TypeOpenAI, APIKey: "dummy-openai-key", BaseURL: server.URL + "/openai",
					Models:             []catwalk.Model{{ID: "gpt-4o", ContextWindow: 128000, DefaultMaxTokens: 1024}},
					SystemPromptPrefix: "Wrong large provider prefix.",
				})
				smallProvider := config.ProviderConfig{
					ID: "small-anthropic", Type: catwalk.TypeAnthropic, APIKey: "dummy-api-key", BaseURL: server.URL,
					Models:             []catwalk.Model{{ID: "claude-haiku-4-5", ContextWindow: 200000, DefaultMaxTokens: 1024}},
					SystemPromptPrefix: prefix,
				}
				if withOAuth {
					smallProvider.APIKey = "Bearer dummy-oauth-token"
					smallProvider.OAuthToken = &oauth.Token{AccessToken: "dummy-oauth-token", ExpiresAt: time.Now().Add(time.Hour).Unix()}
				}
				providers.Set(smallProvider.ID, smallProvider)
				cfg := config.NewTestStore(&config.Config{
					Providers: providers,
					Models: map[config.SelectedModelType]config.SelectedModel{
						config.SelectedModelTypeLarge: {Provider: "large-openai", Model: "gpt-4o"},
						config.SelectedModelTypeSmall: {Provider: smallProvider.ID, Model: "claude-haiku-4-5"},
					},
					Options: &config.Options{ProjectDirectory: workingDir, DisableAutoSummarize: true},
				})
				c := &coordinator{
					cfg: cfg, sessions: sessions, messages: messages,
					permissions: permission.NewPermissionService(workingDir, config.YoloStandard, nil, nil),
				}
				parent, err := sessions.Create(t.Context(), "Fetch parent", workingDir)
				require.NoError(t, err)
				parentMessage, err := messages.Create(t.Context(), parent.ID, message.CreateMessageParams{Role: message.Assistant})
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				ctx = context.WithValue(ctx, tools.SessionIDContextKey, parent.ID)
				ctx = context.WithValue(ctx, tools.MessageIDContextKey, parentMessage.ID)
				tool, err := c.agenticFetchTool(ctx, server.Client())
				require.NoError(t, err)
				result, err := tool.Run(ctx, fantasy.ToolCall{ID: "fetch_call", Name: tools.AgenticFetchToolName, Input: fmt.Sprintf(`{"prompt":%q}`, userPrompt)})
				require.NoError(t, err)
				require.False(t, result.IsError, "%+v", result)
				require.Equal(t, finalOutput, result.Content)
				require.EqualValues(t, 2, count.Load())
				var firstSystem, firstUser string
				for i := range 2 {
					body := <-requests
					require.Equal(t, "claude-haiku-4-5", body.Model)
					require.True(t, body.Stream)
					var systemTexts []string
					for _, part := range body.System {
						systemTexts = append(systemTexts, part.Text)
					}
					systemText := strings.Join(systemTexts, "\n")
					require.NotEmpty(t, body.Messages)
					require.Equal(t, "user", body.Messages[0].Role)
					var userTexts []string
					for _, part := range body.Messages[0].Content {
						userTexts = append(userTexts, part.Text)
					}
					userText := strings.Join(userTexts, "")
					allText := systemText + userText
					require.Equal(t, 1, strings.Count(allText, prefix))
					require.Equal(t, 1, strings.Count(allText, "You are a web content analysis agent."))
					require.Equal(t, 1, strings.Count(userText, userPrompt))
					require.NotContains(t, allText, "Wrong large provider prefix.")
					if withOAuth {
						require.Equal(t, 1, strings.Count(allText, "x-anthropic-billing-header:"))
						require.Equal(t, 1, strings.Count(allText, AnthropicIdentityPrefix))
						require.GreaterOrEqual(t, len(systemTexts), 2)
						require.Equal(t, AnthropicIdentityPrefix, systemTexts[1])
						if mode == SystemModeB {
							require.Len(t, systemTexts, 2)
							require.Contains(t, userText, prefix)
							require.Contains(t, userText, "You are a web content analysis agent.")
							require.Len(t, userTexts, 3)
							require.Contains(t, userTexts[1], "<system_reminder>")
							require.Contains(t, userTexts[2], userPrompt)
							require.Equal(t, anthropicoauth.BuildBillingValue(strings.Join(userTexts[:2], "")), systemTexts[0])
						} else {
							require.Equal(t, prefix, systemTexts[2])
							require.Contains(t, systemText, "You are a web content analysis agent.")
							require.NotContains(t, userText, prefix)
							require.Equal(t, anthropicoauth.BuildBillingValue(strings.Join(systemTexts[2:], "\n")), systemTexts[0])
						}
					} else {
						require.NotContains(t, allText, "x-anthropic-billing-header:")
						require.NotContains(t, allText, AnthropicIdentityPrefix)
						require.Equal(t, prefix, systemTexts[0])
						require.Contains(t, systemText, "You are a web content analysis agent.")
						require.NotContains(t, userText, prefix)
					}
					if i == 0 {
						firstSystem, firstUser = systemText, userText
					} else {
						require.Equal(t, firstSystem, systemText)
						require.Equal(t, firstUser, userText)
						var results []block
						for _, msg := range body.Messages {
							for _, part := range msg.Content {
								if part.Type == "tool_result" {
									results = append(results, part)
								}
							}
						}
						require.Len(t, results, 1)
						require.Equal(t, "glob_fetch", results[0].ToolUseID)
						require.False(t, results[0].IsError)
						require.Contains(t, string(results[0].Content), "No files found")
					}
				}
			})
		}
	}
}

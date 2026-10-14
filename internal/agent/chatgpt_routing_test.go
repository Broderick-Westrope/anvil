package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/stretchr/testify/require"
)

// capturedRequest is what the stub OpenAI server saw.
type capturedRequest struct {
	path    string
	headers http.Header
	body    map[string]any
}

func openAIStub(t *testing.T) (*httptest.Server, func() capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var got capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		got = capturedRequest{path: r.URL.Path, headers: r.Header.Clone(), body: body}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"stub","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

// streamOnce builds the provider for pc and sends one request through it.
func streamOnce(t *testing.T, pc config.ProviderConfig, modelID string) {
	t.Helper()
	cfg, err := config.Init(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	cfg.Config().Providers.Set(pc.ID, pc)
	c := &coordinator{cfg: cfg}

	provider, err := c.buildProvider(pc, config.SelectedModel{Provider: pc.ID, Model: modelID}, false)
	require.NoError(t, err)
	model, err := provider.LanguageModel(t.Context(), modelID)
	require.NoError(t, err)
	maxTokens := int64(1000)
	stream, err := model.Stream(t.Context(), fantasy.Call{
		Prompt:          fantasy.Prompt{fantasy.NewUserMessage("hi")},
		MaxOutputTokens: &maxTokens,
	})
	if err == nil {
		for range stream {
		}
	}
}

// Not parallel: swaps codexBaseURL.
func TestChatGPTLoginRoutesToTheCodexBackend(t *testing.T) {
	srv, got := openAIStub(t)
	original := codexBaseURL
	codexBaseURL = srv.URL
	t.Cleanup(func() { codexBaseURL = original })

	streamOnce(t, config.ProviderConfig{
		ID:         "openai",
		Type:       catwalk.TypeOpenAI,
		BaseURL:    "https://api.openai.com/v1",
		OAuthToken: &oauth.Token{AccessToken: "chatgpt-at", AccountID: "acct-1"},
	}, "gpt-6-codex-future")

	req := got()
	require.Equal(t, "/responses", req.path, "every Codex request uses the Responses API, even for names the heuristic doesn't know")
	require.Equal(t, "anvil", req.headers.Get("originator"))
	// The stub is not chatgpt.com, so the transport's leak guard must
	// strip the ChatGPT credentials before they reach it.
	require.Empty(t, req.headers.Get("Authorization"), "ChatGPT credentials must never reach a non-OpenAI host")
	require.Empty(t, req.headers.Get("chatgpt-account-id"))
}

func TestOpenAIAPIKeyIsUnchanged(t *testing.T) {
	t.Parallel()
	srv, got := openAIStub(t)

	streamOnce(t, config.ProviderConfig{
		ID:      "openai",
		Type:    catwalk.TypeOpenAI,
		BaseURL: srv.URL,
		APIKey:  "sk-key",
	}, "gpt-5")

	req := got()
	require.Equal(t, "Bearer sk-key", req.headers.Get("Authorization"))
	require.Empty(t, req.headers.Get("originator"), "API-key requests are not tagged as Codex traffic")
	require.Equal(t, float64(1000), req.body["max_output_tokens"], "only the Codex transport strips max_output_tokens")
}

// An OAuth token on some other openai-type provider (a custom one) is not
// a ChatGPT login and must not be sent to the Codex backend.
func TestOnlyTheOpenAIProviderUsesChatGPTRouting(t *testing.T) {
	t.Parallel()
	srv, got := openAIStub(t)

	streamOnce(t, config.ProviderConfig{
		ID:         "my-openai-proxy",
		Type:       catwalk.TypeOpenAI,
		BaseURL:    srv.URL,
		APIKey:     "proxy-key",
		OAuthToken: &oauth.Token{AccessToken: "unrelated"},
	}, "gpt-5")

	req := got()
	require.Equal(t, "Bearer proxy-key", req.headers.Get("Authorization"))
	require.Empty(t, req.headers.Get("originator"))
}

func TestFindModelPrefersTheSubscriptionCatalog(t *testing.T) {
	t.Parallel()

	token := &oauth.Token{AccessToken: "at"}
	pc := config.ProviderConfig{
		ID:            "openai",
		OAuthToken:    token,
		Models:        []catwalk.Model{{ID: "gpt-5.1", ContextWindow: 1}, {ID: "api-only"}},
		ChatGPTModels: []catwalk.Model{{ID: "gpt-5.1", ContextWindow: 2}, {ID: "gpt-5.1-codex"}},
	}

	require.Equal(t, int64(2), pc.FindModel("gpt-5.1").ContextWindow, "the plan's entry wins")
	require.NotNil(t, pc.FindModel("gpt-5.1-codex"))
	require.Nil(t, pc.FindModel("api-only"), "a ChatGPT login can't serve API-only models")

	pc.OAuthToken = nil
	require.NotNil(t, pc.FindModel("api-only"))
	require.Nil(t, pc.FindModel("gpt-5.1-codex"))
}

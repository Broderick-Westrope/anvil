package branchfixture

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

type Request struct {
	Body          string
	Authorization string
}

type Response struct {
	RefreshToken string
	Status       int
	Text         string
	InputTokens  int
	ToolName     string
	ToolInput    string
	Started      chan<- struct{}
	Release      <-chan struct{}
	Done         chan<- struct{}
}

type Provider struct {
	tokenPath string
	mu        sync.Mutex
	responses []Response
	requests  []Request
	stopped   chan struct{}
	stopOnce  sync.Once
}

func (p *Provider) Enqueue(responses ...Response) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append(p.responses, responses...)
}

func (p *Provider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.requests...)
}

func (p *Provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		http.Error(w, "unexpected provider request", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, Request{Body: string(body), Authorization: r.Header.Get("Authorization")})
	response := Response{Text: "Local response", InputTokens: 10}
	if len(p.responses) > 0 {
		response = p.responses[0]
		p.responses = p.responses[1:]
	}
	p.mu.Unlock()
	if response.Done != nil {
		defer close(response.Done)
	}
	if response.Started != nil {
		close(response.Started)
	}
	if response.Release != nil {
		select {
		case <-response.Release:
		case <-r.Context().Done():
			return
		case <-p.stopped:
			return
		}
	}
	if response.RefreshToken != "" {
		data, _ := json.Marshal(map[string]any{"providers": map[string]any{"anthropic": map[string]any{"oauth": map[string]any{"access_token": response.RefreshToken, "refresh_token": response.RefreshToken + "-refresh", "expires_in": 3600, "expires_at": 9999999999}}}})
		if err := os.WriteFile(p.tokenPath, data, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if response.Status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.Status)
		fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"scripted provider failure"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(name string, data any) {
		encoded, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
	}
	event("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_local", "type": "message", "role": "assistant", "model": "claude-local", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": response.InputTokens, "output_tokens": 0}}})
	stop := "end_turn"
	if response.ToolName != "" {
		stop = "tool_use"
		event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "tool_local", "name": response.ToolName, "input": map[string]any{}}})
		event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": response.ToolInput}})
	} else {
		event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": response.Text}})
	}
	event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{"type": "message_stop"})
}

type Fixture struct {
	Conn        *sql.DB
	Queries     *db.Queries
	Messages    message.Service
	Sessions    session.Service
	Coordinator agent.Coordinator
	Workspace   *workspace.AppWorkspace
	Config      *config.ConfigStore
	Provider    *Provider
	Context     context.Context
}

func New(t *testing.T) *Fixture {
	t.Helper()
	originalTransport := http.DefaultTransport
	transport := originalTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("fixture blocks non-local HTTP host %q", host)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
	t.Cleanup(transport.CloseIdleConnections)
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "ANVIL_GLOBAL_CONFIG", "ANVIL_GLOBAL_DATA"} {
		t.Setenv(key, t.TempDir())
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	t.Setenv("DO_NOT_TRACK", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	workingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "anvil.json"), []byte(`{"agents":{"orchestrator":{"tools":["todos"]}}}`), 0o600))
	cfg, err := config.Init(workingDir, t.TempDir(), false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(config.GlobalConfigData(), []byte(`{"providers":{"anthropic":{"oauth":{"access_token":"fresh-token","refresh_token":"fresh-refresh","expires_in":3600,"expires_at":9999999999}}}}`), 0o600))
	provider := &Provider{stopped: make(chan struct{}), tokenPath: config.GlobalConfigData()}
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	t.Cleanup(provider.Stop)
	cfg.Config().Providers = csync.NewMap[string, config.ProviderConfig]()
	cfg.Config().Providers.Set("anthropic", config.ProviderConfig{ID: "anthropic", Type: catwalk.TypeAnthropic, BaseURL: server.URL, APIKey: "local-key", Models: []catwalk.Model{{ID: "claude-local", ContextWindow: 200000, DefaultMaxTokens: 1024, SupportsImages: true}}})
	cfg.Config().Models = map[config.SelectedModelType]config.SelectedModel{
		config.SelectedModelTypeLarge: {Provider: "anthropic", Model: "claude-local"},
		config.SelectedModelTypeSmall: {Provider: "anthropic", Model: "claude-local"},
	}
	no := false
	cfg.Config().Options.AutoLSP = &no
	cfg.Config().Options.DisableAutoSummarize = false
	cfg.Config().Options.ContextPaths = nil
	cfg.Config().Options.SkillsPaths = nil
	cfg.Config().LSP = nil
	cfg.Config().MCP = nil
	cfg.Config().Plugins = nil
	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	queries := db.New(conn)
	messages := message.NewService(queries, message.WithConn(conn))
	sessions := session.NewService(queries, conn)
	permissions := permission.NewPermissionService(cfg.WorkingDir(), config.YoloOff, nil, cfg)
	tracker := filetracker.NewService(queries)
	manager := lsp.NewManager(cfg)
	coord, err := agent.NewCoordinator(ctx, cfg, sessions, messages, permissions, tracker, manager, nil)
	require.NoError(t, err)
	w := workspace.NewAppWorkspace(&app.App{Sessions: sessions, Messages: messages, Permissions: permissions, FileTracker: tracker, Queries: queries, LSPManager: manager, AgentCoordinator: coord}, cfg)
	t.Cleanup(func() {
		cancel()
		provider.Stop()
		coord.CancelAll()
		coord.WaitBackgroundJobs()
		require.NoError(t, messages.FlushAll(context.Background()))
	})
	return &Fixture{Conn: conn, Queries: queries, Messages: messages, Sessions: sessions, Coordinator: coord, Workspace: w, Config: cfg, Provider: provider, Context: ctx}
}

func (p *Provider) Stop() { p.stopOnce.Do(func() { close(p.stopped) }) }

package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

const (
	stableLargeModel = "claude-stable-large"
	stableSmallModel = "claude-stable-small"
)

// recordingAnthropicServer answers every Messages API call with a short
// text reply and records the bodies of requests for stableLargeModel, so
// title generation on the small model doesn't count.
type recordingAnthropicServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newRecordingAnthropicServer(t *testing.T) *recordingAnthropicServer {
	t.Helper()
	s := &recordingAnthropicServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var head struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &head); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if head.Model == stableLargeModel {
			s.mu.Lock()
			s.bodies = append(s.bodies, body)
			s.mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range [][2]string{
			{"message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`, head.Model)},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev[0], ev[1])
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recordingAnthropicServer) recorded() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.bodies...)
}

// cacheRequest is the part of a Messages API request that prompt caching
// keys on, with cache_control markers removed. Markers move to the newest
// messages on every request and don't change what is cached.
type cacheRequest struct {
	System   string
	Tools    string
	Messages []string
}

func parseCacheRequest(t *testing.T, body []byte) cacheRequest {
	t.Helper()
	var raw struct {
		System   any   `json:"system"`
		Tools    any   `json:"tools"`
		Messages []any `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(body, &raw))
	canonical := func(v any) string {
		out, err := json.Marshal(stripCacheControl(v))
		require.NoError(t, err)
		return string(out)
	}
	req := cacheRequest{System: canonical(raw.System), Tools: canonical(raw.Tools)}
	for _, m := range raw.Messages {
		req.Messages = append(req.Messages, canonical(m))
	}
	return req
}

// firstDifference shows both strings around the first byte where they
// differ.
func firstDifference(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	window := func(s string) string {
		return s[max(0, i-80):min(len(s), i+80)]
	}
	return fmt.Sprintf("at byte %d\n  before: %q\n  after:  %q", i, window(a), window(b))
}

func stripCacheControl(v any) any {
	switch v := v.(type) {
	case map[string]any:
		delete(v, "cache_control")
		for k, child := range v {
			v[k] = stripCacheControl(child)
		}
	case []any:
		for i, child := range v {
			v[i] = stripCacheControl(child)
		}
	}
	return v
}

// TestRequestPrefixStableAcrossRunsAndRestart sends prompts through a real
// coordinator, rebuilds it as a restart would, and checks that every
// request repeats the previous one's system prompt and tools exactly and
// extends its message history. Anything that reorders or rewrites the
// prompt between runs invalidates the provider's prompt cache. Each input
// has several entries so ordering bugs, such as ranging over a map, show
// up.
func TestRequestPrefixStableAcrossRunsAndRestart(t *testing.T) {
	connectMCPServers(t, map[string]string{
		"test-stable-mcp-a": "Instructions from server A.",
		"test-stable-mcp-b": "Instructions from server B.",
		"test-stable-mcp-c": "Instructions from server C.",
		"test-stable-mcp-d": "Instructions from server D.",
	})

	env := testEnv(t)
	server := newRecordingAnthropicServer(t)

	contextFiles := []string{"ZETA.md", "alpha.md", "Mid.md", "beta.md", "yankee.md", "Echo.md", "kilo.md", "delta.md", "Oscar.md", "golf.md"}
	for _, name := range contextFiles {
		require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, name), []byte("Context from "+name+"."), 0o644))
	}

	pluginDir := filepath.Join(t.TempDir(), "plug")
	for _, name := range []string{"zulu-skill", "alpha-skill", "mike-skill", "bravo-skill"} {
		dir := filepath.Join(pluginDir, "skills", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		content := fmt.Sprintf("---\nname: %s\ndescription: Use for %s work.\n---\nBody of %s.\n", name, name, name)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "agents"), 0o755))
	for _, name := range []string{"zulu", "alpha", "mike", "bravo"} {
		content := fmt.Sprintf("---\nrole: %s role\ndelegate_when: %s work\n---\nYou are %s.\n", name, name, name)
		require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "agents", name+".md"), []byte(content), 0o644))
	}

	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.Config().Providers.Set("stable-anthropic", config.ProviderConfig{
		ID: "stable-anthropic", Type: catwalk.TypeAnthropic, APIKey: "dummy-api-key", BaseURL: server.URL,
		Models: []catwalk.Model{
			{ID: stableLargeModel, ContextWindow: 200000, DefaultMaxTokens: 1024},
			{ID: stableSmallModel, ContextWindow: 200000, DefaultMaxTokens: 1024},
		},
	})
	cfg.Config().Models = map[config.SelectedModelType]config.SelectedModel{
		config.SelectedModelTypeLarge: {Provider: "stable-anthropic", Model: stableLargeModel},
		config.SelectedModelTypeSmall: {Provider: "stable-anthropic", Model: stableSmallModel},
	}
	cfg.Config().Options.ContextPaths = contextFiles
	cfg.Config().Plugins = []config.PluginConfig{{Path: pluginDir}}

	sess, err := env.sessions.Create(t.Context(), "Stable", env.workingDir)
	require.NoError(t, err)

	run := func(prompts ...string) {
		coord, err := NewCoordinator(t.Context(), cfg, env.sessions, env.messages, env.permissions,
			*env.filetracker, nil, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		t.Cleanup(coord.(*coordinator).WaitBackgroundJobs)
		for _, prompt := range prompts {
			_, err := coord.Run(t.Context(), sess.ID, prompt)
			require.NoError(t, err)
		}
	}
	// A shuffle repeats the previous order by chance some of the time, so
	// several restarts with two runs each make a missed one unlikely.
	const restarts, runsPerStart = 6, 2
	for start := range restarts {
		run(fmt.Sprintf("start %d run 1", start), fmt.Sprintf("start %d run 2", start))
	}

	bodies := server.recorded()
	require.Len(t, bodies, restarts*runsPerStart)
	reqs := make([]cacheRequest, len(bodies))
	for i, body := range bodies {
		reqs[i] = parseCacheRequest(t, body)
	}

	// Guard against a vacuous pass: every input must reach the request.
	// Failures print only the missing text; the full prompt is too long to
	// read in test output.
	for _, want := range []string{
		"Context from ZETA.md.", "Context from beta.md.",
		"Instructions from server A.", "Instructions from server D.",
		"zulu-skill", "bravo-skill", "alpha role", "mike role",
	} {
		require.True(t, strings.Contains(reqs[0].System, want), "system prompt is missing %q", want)
	}
	for _, want := range []string{"stable-mcp-a_ping", "stable-mcp-d_ping"} {
		require.True(t, strings.Contains(strings.ToLower(reqs[0].Tools), want), "tools are missing %q", want)
	}

	for i := 1; i < len(reqs); i++ {
		prev, next := reqs[i-1], reqs[i]
		require.True(t, prev.System == next.System, "request %d changed the system prompt:\n%s", i, firstDifference(prev.System, next.System))
		require.True(t, prev.Tools == next.Tools, "request %d changed the tools:\n%s", i, firstDifference(prev.Tools, next.Tools))
		require.Greater(t, len(next.Messages), len(prev.Messages))
		require.Equal(t, prev.Messages, next.Messages[:len(prev.Messages)], "request %d rewrote earlier messages", i)
	}
}

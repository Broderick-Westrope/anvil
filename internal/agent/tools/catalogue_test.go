package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// catalogueFixture is one live MCP server reached through the catalogue.
type catalogueFixture struct {
	name  string
	cfg   *config.ConfigStore
	calls atomic.Int32
	tools []fantasy.AgentTool

	mu  sync.Mutex
	ran []fantasy.ToolCall
}

type catalogueServerTool struct {
	name, description string
	schema            map[string]any
}

func objectSchema(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	logSearchTool = catalogueServerTool{
		name:        "search_logs",
		description: "Search log events. Supports filters.",
		schema:      objectSchema([]string{"query"}, map[string]any{"query": map[string]any{"type": "string"}}),
	}
	loadSkillTool = catalogueServerTool{
		name:        "load_skill",
		description: "Load a skill before using other tools.",
		schema:      objectSchema([]string{"telemetry"}, map[string]any{"telemetry": map[string]any{"type": "string"}}),
	}
	listSkillsTool = catalogueServerTool{
		name:        "list_skills",
		description: "List skills.",
		schema:      objectSchema([]string{"telemetry"}, map[string]any{"telemetry": map[string]any{"type": "string"}}),
	}
	metricsTool = catalogueServerTool{
		name:        "get_metrics",
		description: "Query metric points.",
		schema:      objectSchema(nil, map[string]any{"metric": map[string]any{"type": "string"}}),
	}
)

func serveCatalogueServer(t *testing.T, f *catalogueFixture, instructions string, serverTools []catalogueServerTool) string {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: f.name}, &sdkmcp.ServerOptions{Instructions: instructions})
	for _, st := range serverTools {
		server.AddTool(&sdkmcp.Tool{Name: st.name, Description: st.description, InputSchema: st.schema},
			func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				f.calls.Add(1)
				return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: st.name + " ran with " + string(req.Params.Arguments)}}}, nil
			})
	}
	ts := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

// newCatalogueFixture serves serverTools as the MCP server name and builds
// the catalogue tools for it. With deferred set, the server is configured
// lazily and left unconnected.
func newCatalogueFixture(t *testing.T, name, instructions string, deferred bool, serverTools ...catalogueServerTool) *catalogueFixture {
	t.Helper()
	renamed := mcp.OAuthRenameEnabled()
	mcp.SetOAuthRename(false)
	t.Cleanup(func() { mcp.SetOAuthRename(renamed) })

	f := &catalogueFixture{name: name}
	url := serveCatalogueServer(t, f, instructions, serverTools)
	m := config.MCPConfig{Type: config.MCPHttp, URL: url}
	if deferred {
		m.LazyDescription = "Lazy " + name + "."
	}
	f.cfg = config.NewTestStore(&config.Config{MCP: config.MCPs{name: m}})
	t.Cleanup(func() {
		_ = mcp.DisableSingle(f.cfg, name)
		mcp.DeleteStateForTest(name)
		mcp.DeleteToolsForTest(name)
	})
	if deferred {
		mcp.SetStateForTest(name, mcp.StateDeferred)
	} else {
		require.NoError(t, mcp.InitializeSingle(t.Context(), name, f.cfg, nil))
	}
	f.tools = NewCatalogueTools(CatalogueOptions{
		Servers: []CatalogueServer{{Name: name, Description: "Logs and metrics."}},
		Config:  f.cfg,
		Tools:   f.serverTools,
	})
	return f
}

// serverTools stands in for the agent's resolver: every registered tool,
// run through a recorder in place of the hook and error wrappers.
func (f *catalogueFixture) serverTools(server string) []CatalogueTool {
	var out []CatalogueTool
	for _, tool := range GetMCPTools(permission.NewPermissionService(f.cfg.WorkingDir(), config.YoloFull, nil, nil), f.cfg, f.cfg.WorkingDir()) {
		if tool.MCP() == server {
			out = append(out, CatalogueTool{MCP: tool, Run: recordingTool{AgentTool: tool, f: f}})
		}
	}
	return out
}

type recordingTool struct {
	fantasy.AgentTool
	f *catalogueFixture
}

func (r recordingTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	r.f.mu.Lock()
	r.f.ran = append(r.f.ran, call)
	r.f.mu.Unlock()
	return r.AgentTool.Run(ctx, call)
}

func (f *catalogueFixture) run(t *testing.T, tool string, input any) fantasy.ToolResponse {
	t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	for _, candidate := range f.tools {
		if candidate.Info().Name == tool {
			ctx := context.WithValue(t.Context(), SessionIDContextKey, "session-1")
			resp, err := candidate.Run(ctx, fantasy.ToolCall{ID: "outer-call", Name: tool, Input: string(raw)})
			require.NoError(t, err)
			return resp
		}
	}
	t.Fatalf("no catalogue tool %q", tool)
	return fantasy.ToolResponse{}
}

func TestCatalogueToolsAreTheThreeFixedTools(t *testing.T) {
	f := newCatalogueFixture(t, "cat-names", "", false, logSearchTool)
	var names []string
	for _, tool := range f.tools {
		names = append(names, tool.Info().Name)
	}
	require.Equal(t, []string{SearchMCPToolsToolName, DescribeMCPToolToolName, CallMCPToolToolName}, names)
	search := f.tools[0].Info().Description
	require.Contains(t, search, "- cat-names: Logs and metrics.")
	require.Contains(t, search, "never callable directly by name")
}

func TestSearchWithServerStartsWithItsInstructionsAndTheSchemasTheyName(t *testing.T) {
	instructions := "Call load_skill and list_skills before invoking other tools. get_metrics is handy too."
	f := newCatalogueFixture(t, "cat-instr", instructions, false, logSearchTool, loadSkillTool, listSkillsTool, metricsTool)

	resp := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-instr", "query": "log search"})

	require.False(t, resp.IsError, resp.Content)
	text := resp.Content
	require.True(t, strings.HasPrefix(text, "<cat-instr-instructions>\n"+instructions), text)
	loadAt := strings.Index(text, `Input schema for load_skill:`)
	listAt := strings.Index(text, `Input schema for list_skills:`)
	require.Positive(t, loadAt)
	require.Greater(t, listAt, loadAt, "schemas follow the order the instructions name the tools")
	require.NotContains(t, text, "Input schema for get_metrics", "tools without required fields aren't appended")
	require.Contains(t, text, "- search_logs: Search log events.")
	require.Less(t, listAt, strings.Index(text, "- search_logs"))
}

func TestSearchAppendsAtMostTwoInstructionSchemasWithinFourKilobytes(t *testing.T) {
	big := catalogueServerTool{name: "big_tool", description: "Big.", schema: objectSchema([]string{"x"}, map[string]any{
		"x": map[string]any{"type": "string", "description": strings.Repeat("y", 4200)},
	})}
	third := catalogueServerTool{name: "third_tool", description: "Third.", schema: loadSkillTool.schema}
	f := newCatalogueFixture(t, "cat-limit", "Use big_tool, load_skill, list_skills and third_tool.", false, big, loadSkillTool, listSkillsTool, third)

	text := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-limit", "query": "skill"}).Content

	require.NotContains(t, text, "Input schema for big_tool", "a schema that would overflow 4 KB is skipped")
	require.Contains(t, text, "Input schema for load_skill")
	require.Contains(t, text, "Input schema for list_skills")
	require.NotContains(t, text, "Input schema for third_tool", "at most two schemas")
}

func TestSearchWithServerAndNoQueryListsEveryName(t *testing.T) {
	f := newCatalogueFixture(t, "cat-all", "", false, logSearchTool, metricsTool, loadSkillTool)

	text := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-all"}).Content

	require.Contains(t, text, "All 3 tools on cat-all:\n- get_metrics\n- load_skill\n- search_logs\n")
}

func TestSearchRanksNameMatchesOverDescriptionMatches(t *testing.T) {
	descOnly := catalogueServerTool{name: "audit_trail", description: "Keeps logs for compliance.", schema: objectSchema(nil, map[string]any{})}
	f := newCatalogueFixture(t, "cat-rank", "", false, descOnly, logSearchTool, metricsTool)

	text := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-rank", "query": "logs"}).Content

	require.Less(t, strings.Index(text, "- search_logs"), strings.Index(text, "- audit_trail"), text)
	require.NotContains(t, text, "get_metrics")
}

func TestSearchWithoutServerDoesNotConnectDeferredServers(t *testing.T) {
	f := newCatalogueFixture(t, "cat-deferred-quiet", "", true, logSearchTool)

	text := f.run(t, SearchMCPToolsToolName, map[string]any{"query": "logs metrics"}).Content

	info, _ := mcp.GetState("cat-deferred-quiet")
	require.Equal(t, mcp.StateDeferred, info.State)
	require.Contains(t, text, `cat-deferred-quiet (Logs and metrics.) isn't connected yet; pass server="cat-deferred-quiet" to search it.`)
}

func TestSearchWithServerConnectsADeferredServer(t *testing.T) {
	f := newCatalogueFixture(t, "cat-deferred", "", true, logSearchTool)

	resp := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-deferred", "query": "logs"})

	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "- search_logs")
}

func TestConcurrentFirstSearchesOfADeferredServerBothFindIt(t *testing.T) {
	f := newCatalogueFixture(t, "cat-deferred-race", "", true, logSearchTool)

	var wg sync.WaitGroup
	results := make([]string, 2)
	for i := range results {
		wg.Go(func() {
			results[i] = f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-deferred-race", "query": "logs"}).Content
		})
	}
	wg.Wait()

	for _, text := range results {
		require.Contains(t, text, "- search_logs")
	}
}

func TestSearchReportsAServerThatCantBeUsed(t *testing.T) {
	tests := map[string]struct {
		state mcp.State
		err   error
		want  string
	}{
		"error":    {state: mcp.StateError, err: errors.New("boom"), want: "MCP 'cat-down' failed to connect: boom"},
		"disabled": {state: mcp.StateDisabled, want: "MCP 'cat-down' is disabled"},
		"auth":     {state: mcp.StateError, err: mcp.ErrNeedsAuth, want: "only a human can renew it"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newCatalogueFixture(t, "cat-down", "", true, logSearchTool)
			mcp.SetStateWithErrorForTest("cat-down", tc.state, tc.err)

			resp := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-down", "query": "logs"})

			require.False(t, resp.IsError)
			require.Contains(t, resp.Content, tc.want)
		})
	}
}

func TestCatalogueRejectsAServerItDoesNotServe(t *testing.T) {
	f := newCatalogueFixture(t, "cat-known", "", false, logSearchTool)

	resp := f.run(t, DescribeMCPToolToolName, map[string]any{"server": "elsewhere", "tools": []string{"x"}})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, `unknown catalogue server "elsewhere", available servers: cat-known`)
}

func TestDescribeReturnsTheRawSchemaUnderEitherName(t *testing.T) {
	nullable := catalogueServerTool{name: "maybe", description: "Maybe.", schema: objectSchema(nil, map[string]any{
		"v": map[string]any{"type": []any{"string", "null"}},
	})}
	f := newCatalogueFixture(t, "cat-describe", "", false, nullable, logSearchTool)

	text := f.run(t, DescribeMCPToolToolName, map[string]any{"server": "cat-describe", "tools": []string{"MCP_cat-describe_maybe", "search_logs", "nope"}}).Content

	require.Contains(t, text, "## maybe\nMaybe.\n\nInput schema:\n")
	require.Contains(t, text, `"type":["string","null"]`, "the schema as the server sent it, not normalised")
	require.Contains(t, text, "## search_logs\n")
	require.Contains(t, text, "## nope\nNo such tool on cat-describe.")
}

func TestCallRunsTheInnerToolUnderItsOwnName(t *testing.T) {
	f := newCatalogueFixture(t, "cat-call", "", false, logSearchTool)

	tests := map[string]any{
		"object": map[string]any{"query": "errors"},
		"string": `{"query":"errors"}`,
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			resp := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-call", "tool": "search_logs", "arguments": args})

			require.False(t, resp.IsError, resp.Content)
			require.Equal(t, `search_logs ran with {"query":"errors"}`, resp.Content)
			last := f.ran[len(f.ran)-1]
			require.Equal(t, fantasy.ToolCall{ID: "outer-call", Name: "mcp_cat-call_search_logs", Input: `{"query":"errors"}`}, last)
		})
	}
}

func TestCallRejectsArgumentsThatBreakTheSchema(t *testing.T) {
	f := newCatalogueFixture(t, "cat-invalid", "Call load_skill first.", false, logSearchTool, loadSkillTool)

	resp := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-invalid", "tool": "search_logs", "arguments": map[string]any{"q": 1}})

	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, InvalidArgumentsPrefix), resp.Content)
	require.NotContains(t, resp.Content, InstructionNamedMarker)
	require.Contains(t, resp.Content, "query")
	require.Contains(t, resp.Content, `Input schema for search_logs:`)
	require.Zero(t, f.calls.Load(), "the server is never contacted")

	named := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-invalid", "tool": "load_skill", "arguments": map[string]any{}})
	require.True(t, strings.HasPrefix(named.Content, InvalidArgumentsPrefix+InstructionNamedMarker), named.Content)
}

func TestCallPassesAnUnresolvableSchemaThrough(t *testing.T) {
	remote := catalogueServerTool{name: "remote_ref", description: "Remote.", schema: map[string]any{
		"type": "object", "properties": map[string]any{"x": map[string]any{"$ref": "https://example.invalid/schema.json"}},
	}}
	f := newCatalogueFixture(t, "cat-remote", "", false, remote)

	resp := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-remote", "tool": "remote_ref", "arguments": map[string]any{"x": 1}})

	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, int32(1), f.calls.Load())
}

func TestCallNamesTheClosestToolsForAnUnknownOne(t *testing.T) {
	f := newCatalogueFixture(t, "cat-unknown", "", false, logSearchTool, metricsTool)

	resp := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-unknown", "tool": "logs_search", "arguments": map[string]any{}})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, `no tool "logs_search" on cat-unknown. Closest matches: search_logs`)
}

func TestCallAcceptsTheServerSlashToolFormSearchPrints(t *testing.T) {
	f := newCatalogueFixture(t, "cat-slash", "", false, logSearchTool)

	resp := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-slash", "tool": "cat-slash/search_logs", "arguments": map[string]any{"query": "x"}})

	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, int32(1), f.calls.Load())
}

func TestDescribeWithNoToolsSaysSo(t *testing.T) {
	f := newCatalogueFixture(t, "cat-describe-none", "", false, logSearchTool)

	resp := f.run(t, DescribeMCPToolToolName, map[string]any{"server": "cat-describe-none", "tools": []string{}})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "tools is empty")
}

func TestCallOnAnUnusableServerIsAnErrorNamingTheCall(t *testing.T) {
	f := newCatalogueFixture(t, "cat-call-down", "", true, logSearchTool)
	mcp.SetStateWithErrorForTest("cat-call-down", mcp.StateError, mcp.ErrNeedsAuth)

	resp := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-call-down", "tool": "search_logs", "arguments": map[string]any{}})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Do not retry call_mcp_tool")
}

func TestSearchSplitsCamelCaseAndIgnoresPlurals(t *testing.T) {
	jira := catalogueServerTool{name: "getJiraIssue", description: "Fetch one.", schema: objectSchema(nil, map[string]any{})}
	f := newCatalogueFixture(t, "cat-camel", "", false, jira, logSearchTool)

	require.Contains(t, f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-camel", "query": "jira issues"}).Content, "- getJiraIssue")
	require.Contains(t, f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-camel", "query": "log"}).Content, "- search_logs")
}

func TestCallWithoutArgumentsSendsAnEmptyObject(t *testing.T) {
	noArgs := catalogueServerTool{name: "whoami", description: "Who.", schema: objectSchema(nil, map[string]any{})}
	f := newCatalogueFixture(t, "cat-noargs", "", false, noArgs)
	for _, tool := range f.tools {
		if tool.Info().Name == CallMCPToolToolName {
			require.NotContains(t, tool.Info().Required, "arguments")
		}
	}

	resp := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-noargs", "tool": "whoami"})

	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "whoami ran with {}", resp.Content)
}

func TestSearchRowsCutLongDescriptionsOnRunes(t *testing.T) {
	long := catalogueServerTool{name: "long_tool", description: "a" + strings.Repeat("é", 250), schema: objectSchema(nil, map[string]any{})}
	f := newCatalogueFixture(t, "cat-runes", "", false, long)

	text := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-runes", "query": "long"}).Content

	require.Contains(t, text, "...\n")
	require.True(t, utf8.ValidString(text))
}

func TestCatalogueToolsNeverSendANullRequiredList(t *testing.T) {
	t.Parallel()
	for _, tool := range NewCatalogueTools(CatalogueOptions{Servers: []CatalogueServer{{Name: "x", Description: "X."}}}) {
		require.NotNil(t, tool.Info().Required, tool.Info().Name)
	}
}

func TestServerNamesMatchInAnyCase(t *testing.T) {
	f := newCatalogueFixture(t, "cat-case", "", false, logSearchTool)

	resp := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "Cat-Case", "query": "logs"})

	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "- search_logs")
}

func TestDescribeSaysWhichNamesItLeftOut(t *testing.T) {
	f := newCatalogueFixture(t, "cat-describe-many", "", false, logSearchTool)
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "search_logs", "k"}

	text := f.run(t, DescribeMCPToolToolName, map[string]any{"server": "cat-describe-many", "tools": names}).Content

	require.Contains(t, text, "Not described (limit 10): search_logs, k")
}

func TestSearchWaitsOutAConnectRetryInsteadOfReportingItsError(t *testing.T) {
	f := newCatalogueFixture(t, "cat-retry", "", true, logSearchTool)
	mcp.SetStateWithErrorForTest("cat-retry", mcp.StateError, errors.New("dial: i/o timeout"))
	mcp.SetConnectingForTest("cat-retry", true)
	t.Cleanup(func() { mcp.SetConnectingForTest("cat-retry", false) })
	go func() {
		time.Sleep(100 * time.Millisecond)
		mcp.SetToolsForTest("cat-retry", &mcp.Tool{Name: "search_logs", Description: "Search logs.", InputSchema: logSearchTool.schema})
		mcp.SetStateForTest("cat-retry", mcp.StateLazy)
		mcp.SetConnectingForTest("cat-retry", false)
	}()

	resp := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-retry", "query": "logs"})

	require.Contains(t, resp.Content, "- search_logs")
}

func TestACancelledConnectIsReportedAsCancelled(t *testing.T) {
	f := newCatalogueFixture(t, "cat-cancel", "", true, logSearchTool)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), SessionIDContextKey, "session-1"))
	cancel()

	search := f.tools[0]
	resp, err := search.Run(ctx, fantasy.ToolCall{ID: "outer", Name: SearchMCPToolsToolName, Input: `{"server":"cat-cancel","query":"logs"}`})

	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, context.Canceled.Error(), resp.Content)
}

func TestAWaiterDoesNotReconnectAfterAnotherCallersConnectFails(t *testing.T) {
	f := newCatalogueFixture(t, "cat-failed-wait", "", true, logSearchTool)
	mcp.SetStateForTest("cat-failed-wait", mcp.StateStarting)
	go func() {
		time.Sleep(100 * time.Millisecond)
		// Both of the other caller's attempts failed transiently.
		mcp.SetStateForTest("cat-failed-wait", mcp.StateDeferred)
	}()

	resp := f.run(t, SearchMCPToolsToolName, map[string]any{"server": "cat-failed-wait", "query": "logs"})

	require.Contains(t, resp.Content, "MCP 'cat-failed-wait' failed to connect; retry later")
	info, _ := mcp.GetState("cat-failed-wait")
	require.Equal(t, mcp.StateDeferred, info.State, "the waiter must not start its own connect")
}

package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"
	"unicode"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
)

const (
	SearchMCPToolsToolName  = "search_mcp_tools"
	DescribeMCPToolToolName = "describe_mcp_tool"
	CallMCPToolToolName     = "call_mcp_tool"
)

// InvalidArgumentsPrefix starts every call_mcp_tool error for arguments
// that break the tool's schema, so the rate can be counted from stored
// messages. InstructionNamedMarker follows it when the server's
// instructions name the tool, so the two kinds can be counted apart.
const (
	InvalidArgumentsPrefix = "Arguments don't match the tool's input schema"
	InstructionNamedMarker = " (a tool the server's instructions name)"
)

const (
	maxSearchRows            = 20
	maxDescribedTools        = 10
	maxInstructionSchemas    = 2
	maxInstructionSchemaSize = 4096
	maxClosestMatches        = 5
)

//go:embed search_mcp_tools.md.tpl
var searchMCPToolsDescriptionTmpl string

//go:embed describe_mcp_tool.md
var describeMCPToolDescription string

//go:embed call_mcp_tool.md
var callMCPToolDescription string

var searchMCPToolsDescriptionTpl = template.Must(
	template.New("searchMCPToolsDescription").Parse(searchMCPToolsDescriptionTmpl),
)

// CatalogueServer is a server the catalogue serves, introduced to the
// model by Description.
type CatalogueServer struct {
	Name        string
	Description string
}

// CatalogueTool is one MCP tool the catalogue can reach for an agent: the
// unwrapped tool for its name, description and schema, and the agent's
// wrapped tool (hooks, error results) that a call runs.
type CatalogueTool struct {
	MCP *Tool
	Run fantasy.AgentTool
}

// CatalogueOptions configures the catalogue tools for one agent.
type CatalogueOptions struct {
	// Servers are the catalogue servers the agent may use. Their order
	// and descriptions go into the search tool's description verbatim,
	// so they must be fixed for the process.
	Servers []CatalogueServer
	// Tools returns the agent's tools on a connected server: allowed,
	// filtered and wrapped exactly as a directly listed tool would be.
	Tools func(server string) []CatalogueTool
	// Config supplies the servers' connect settings.
	Config *config.ConfigStore
}

type catalogue struct {
	opts         CatalogueOptions
	descriptions map[string]string
}

// NewCatalogueTools returns search_mcp_tools, describe_mcp_tool and
// call_mcp_tool for the servers in opts.
func NewCatalogueTools(opts CatalogueOptions) []fantasy.AgentTool {
	c := &catalogue{opts: opts, descriptions: map[string]string{}}
	for _, server := range opts.Servers {
		c.descriptions[server.Name] = server.Description
	}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return []fantasy.AgentTool{
		&catalogueTool{info: fantasy.ToolInfo{
			Name:        SearchMCPToolsToolName,
			Description: searchDescription(opts.Servers),
			Parameters: map[string]any{
				"server": str("Server to search. Omit to search every connected server."),
				"query":  str("Keywords describing what the tool does. Omit, with server set, to list every tool name on the server."),
			},
			// Never nil: a nil list is sent as "required": null, which
			// OpenAI rejects.
			Required: []string{},
		}, run: c.search},
		&catalogueTool{info: fantasy.ToolInfo{
			Name:        DescribeMCPToolToolName,
			Description: describeMCPToolDescription,
			Parameters: map[string]any{
				"server": str("Server name."),
				"tools": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Up to 10 tool names, as search_mcp_tools returns them.",
				},
			},
			Required: []string{"server", "tools"},
		}, run: c.describe},
		&catalogueTool{info: fantasy.ToolInfo{
			Name:        CallMCPToolToolName,
			Description: callMCPToolDescription,
			Parameters: map[string]any{
				"server": str("Server name."),
				"tool":   str("Tool name, as search_mcp_tools returns it."),
				// Untyped: a JSON object or a string holding one. A
				// property-less object type is what Gemini rejects.
				"arguments": map[string]any{"description": "The tool's arguments: a JSON object matching its input schema."},
			},
			// arguments is optional: a tool without parameters gets {}.
			Required: []string{"server", "tool"},
		}, run: c.call},
	}
}

// searchDescription lists the catalogue servers. It uses text/template:
// descriptions come from config and must reach the model unescaped.
func searchDescription(servers []CatalogueServer) string {
	var out strings.Builder
	if err := searchMCPToolsDescriptionTpl.Execute(&out, struct{ Servers []CatalogueServer }{servers}); err != nil {
		panic("failed to execute tool description template: " + err.Error())
	}
	return out.String()
}

// catalogueTool is a fantasy.AgentTool with a hand-written schema, since
// call_mcp_tool's arguments parameter has no type.
type catalogueTool struct {
	info fantasy.ToolInfo
	run  func(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error)
	opts fantasy.ProviderOptions
}

func (t *catalogueTool) Info() fantasy.ToolInfo                          { return t.info }
func (t *catalogueTool) ProviderOptions() fantasy.ProviderOptions        { return t.opts }
func (t *catalogueTool) SetProviderOptions(opts fantasy.ProviderOptions) { t.opts = opts }

func (t *catalogueTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return t.run(ctx, call)
}

// canonicalServer returns the served server matching name in any case, or
// name: models echo the case they last saw, and the OAuth rename
// capitalises server names.
func (c *catalogue) canonicalServer(name string) string {
	if _, ok := c.descriptions[name]; ok {
		return name
	}
	for _, server := range c.opts.Servers {
		if strings.EqualFold(server.Name, name) {
			return server.Name
		}
	}
	return name
}

// errUnusable carries a non-error explanation of why a server can't be
// used right now: the call was well formed, so it isn't the model's
// mistake to retry.
type errUnusable struct{ msg string }

func (e errUnusable) Error() string { return e.msg }

// serverTools resolves server for this agent: it checks the agent may use
// it, connects it if deferred or waits if it is starting, and returns the
// server's instructions and the agent's tools on it, in name order.
// caller names the catalogue tool asking, for the auth-expiry text.
func (c *catalogue) serverTools(ctx context.Context, server, caller string) (string, []CatalogueTool, error) {
	if _, ok := c.descriptions[server]; !ok {
		var names []string
		for _, s := range c.opts.Servers {
			names = append(names, s.Name)
		}
		return "", nil, fmt.Errorf("unknown catalogue server %q, available servers: %s", server, strings.Join(names, ", "))
	}
	instructions, err := c.ready(ctx, server, caller)
	if err != nil {
		return "", nil, err
	}
	tools := c.opts.Tools(server)
	slices.SortFunc(tools, func(a, b CatalogueTool) int {
		return strings.Compare(a.MCP.MCPToolName(), b.MCP.MCPToolName())
	})
	return instructions, tools, nil
}

// ready brings server to a usable state and returns its instructions.
// It waits at most once for another caller's connect and connects at
// most once itself: a waiter whose connect failed reports it rather than
// trying again.
func (c *catalogue) ready(ctx context.Context, server, caller string) (string, error) {
	info, ok := mcp.GetState(server)
	if !ok {
		return "", errUnusable{fmt.Sprintf("MCP '%s' isn't running in this process", server)}
	}
	// Starting, or an error between a transient failure and its retry:
	// another caller is connecting it.
	if info.State == mcp.StateStarting || info.State == mcp.StateError && mcp.IsConnecting(server) {
		if err := c.settle(ctx, server); err != nil {
			return "", err
		}
	} else if info.State == mcp.StateDeferred {
		if err := c.connect(ctx, server, caller); err != nil {
			return "", err
		}
	}
	info, _ = mcp.GetState(server)
	switch info.State {
	case mcp.StateConnected:
		if info.Client == nil {
			return "", nil
		}
		return info.Client.InitializeResult().Instructions, nil
	case mcp.StateDisabled:
		return "", errUnusable{fmt.Sprintf("MCP '%s' is disabled. Tell the user if the task needs it.", server)}
	case mcp.StateError:
		return "", stateErrorProblem(server, info, caller)
	case mcp.StateDeferred:
		return "", errUnusable{fmt.Sprintf("MCP '%s' failed to connect; retry later", server)}
	default:
		return "", errUnusable{fmt.Sprintf("MCP '%s' is still starting, retry shortly", server)}
	}
}

// authRequiredMessage tells the model that server's OAuth token must be
// renewed by a human. The catalogue returns it as a result, not an error:
// the call wasn't malformed, and an error pushes models toward retry
// loops. The wording tells the model not to retry tool this turn and not
// to try authenticating itself.
func authRequiredMessage(server, tool string) string {
	return fmt.Sprintf(
		"MCP '%s' cannot be used: its authentication has expired "+
			"and only a human can renew it. The user has been notified "+
			"in the UI. Do not retry %s for '%s' this turn and "+
			"do not attempt to authenticate yourself. Tell the user "+
			"that '%s' needs re-authentication, then continue with "+
			"whatever part of the task does not need it."+
			" (If this is a non-interactive session, the user must run "+
			"`anvil mcp auth %s`.)",
		server, tool, server, server, server)
}

func stateErrorProblem(server string, info mcp.ClientInfo, caller string) error {
	if info.NeedsAuth {
		return errUnusable{authRequiredMessage(server, caller)}
	}
	reason := "unknown error"
	if info.Error != nil {
		reason = info.Error.Error()
	}
	return errUnusable{fmt.Sprintf("MCP '%s' failed to connect: %s. A human can reconnect it from the MCP palette; don't retry it this turn.", server, reason)}
}

func (c *catalogue) connect(ctx context.Context, server, caller string) error {
	err := mcp.ConnectDeferred(ctx, server, c.opts.Config)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		// Cancelled, not a failure of the server.
		return ctx.Err()
	case errors.Is(err, mcp.ErrAlreadyConnecting):
		return c.settle(ctx, server)
	case mcp.NeedsAuth(err):
		return errUnusable{authRequiredMessage(server, caller)}
	default:
		return errUnusable{fmt.Sprintf("Failed to connect MCP '%s': %s", server, err)}
	}
}

// settle waits for another caller's connect to finish, bounded by two
// connect timeouts: ConnectDeferred retries a transient failure once.
func (c *catalogue) settle(ctx context.Context, server string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*mcp.ConnectTimeout(c.opts.Config.Config().MCP[server]))
	defer cancel()
	if _, err := mcp.WaitSettled(ctx, server); err != nil {
		if parent := context.Cause(ctx); parent != nil && !errors.Is(parent, context.DeadlineExceeded) {
			return parent
		}
		return errUnusable{fmt.Sprintf("MCP '%s' is still starting, retry shortly", server)}
	}
	return nil
}

// searchProblem turns a resolve error into a search result: a server that
// can't be used now is reported as a result, so it never reads as "no
// such tools", and anything else as an error. describe and call report
// both as errors: they didn't do what was asked.
func searchProblem(err error) fantasy.ToolResponse {
	var unusable errUnusable
	if errors.As(err, &unusable) {
		return fantasy.NewTextResponse(unusable.msg)
	}
	return fantasy.NewTextErrorResponse(err.Error())
}

type searchParams struct {
	Server string `json:"server"`
	Query  string `json:"query"`
}

func (c *catalogue) search(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params searchParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid input: " + err.Error()), nil
	}
	params.Server = c.canonicalServer(params.Server)
	query := searchTerms(params.Query)
	if params.Server == "" {
		return fantasy.NewTextResponse(c.searchConnected(ctx, query)), nil
	}
	instructions, tools, err := c.serverTools(ctx, params.Server, SearchMCPToolsToolName)
	if err != nil {
		return searchProblem(err), nil
	}
	var out strings.Builder
	if instructions != "" {
		fmt.Fprintf(&out, "<%s-instructions>\n%s\n</%s-instructions>\n\n", params.Server, instructions, params.Server)
		for _, tool := range instructionSchemas(instructions, tools) {
			fmt.Fprintf(&out, "Input schema for %s:\n%s\n\n", tool.MCP.MCPToolName(), compactSchema(tool.MCP))
		}
	}
	if len(query) == 0 {
		fmt.Fprintf(&out, "All %d tools on %s:\n", len(tools), params.Server)
		for _, tool := range tools {
			fmt.Fprintf(&out, "- %s\n", tool.MCP.MCPToolName())
		}
		return fantasy.NewTextResponse(out.String()), nil
	}
	writeMatches(&out, rankTools(query, map[string][]CatalogueTool{params.Server: tools}), false)
	return fantasy.NewTextResponse(out.String()), nil
}

// searchConnected searches every catalogue server that is already
// connected. Connecting all of them for one vague query could start (or
// prompt OAuth for) every server, so the rest are only matched by their
// description.
func (c *catalogue) searchConnected(ctx context.Context, query []string) string {
	var out strings.Builder
	if len(query) == 0 {
		out.WriteString("Pass a query, or a server to list its tools. Servers:\n")
		for _, server := range c.opts.Servers {
			fmt.Fprintf(&out, "- %s: %s\n", server.Name, server.Description)
		}
		return out.String()
	}
	byServer := map[string][]CatalogueTool{}
	for _, server := range c.opts.Servers {
		info, _ := mcp.GetState(server.Name)
		if info.State != mcp.StateConnected {
			if matchesAny(query, searchTerms(server.Name+" "+server.Description)) {
				fmt.Fprintf(&out, "%s (%s) isn't connected yet; pass server=%q to search it.\n", server.Name, server.Description, server.Name)
			}
			continue
		}
		if _, tools, err := c.serverTools(ctx, server.Name, SearchMCPToolsToolName); err == nil {
			byServer[server.Name] = tools
		}
	}
	writeMatches(&out, rankTools(query, byServer), true)
	return out.String()
}

type searchMatch struct {
	server string
	tool   *Tool
	score  int
}

// rankTools scores each tool by lowercase term overlap with query, name
// matches weighing three times description matches, and returns the
// matches best first, ties broken by server and tool name.
func rankTools(query []string, byServer map[string][]CatalogueTool) []searchMatch {
	var matches []searchMatch
	for server, tools := range byServer {
		for _, tool := range tools {
			nameTerms := searchTerms(tool.MCP.MCPToolName())
			descTerms := searchTerms(tool.MCP.tool.Description)
			score := 0
			for _, term := range query {
				if slices.Contains(nameTerms, term) {
					score += 3
				}
				if slices.Contains(descTerms, term) {
					score++
				}
			}
			if score > 0 {
				matches = append(matches, searchMatch{server: server, tool: tool.MCP, score: score})
			}
		}
	}
	slices.SortFunc(matches, func(a, b searchMatch) int {
		if a.score != b.score {
			return b.score - a.score
		}
		if a.server != b.server {
			return strings.Compare(a.server, b.server)
		}
		return strings.Compare(a.tool.MCPToolName(), b.tool.MCPToolName())
	})
	return matches
}

func writeMatches(out *strings.Builder, matches []searchMatch, withServer bool) {
	if len(matches) == 0 {
		out.WriteString("No matching tools. Try other keywords, or pass server with no query to list every tool name on it.\n")
		return
	}
	for _, match := range matches[:min(len(matches), maxSearchRows)] {
		name := match.tool.MCPToolName()
		if withServer {
			name = match.server + "/" + name
		}
		fmt.Fprintf(out, "- %s: %s\n", name, firstSentence(match.tool.tool.Description))
	}
}

// searchTerms splits s into lowercase terms at anything that isn't a
// letter or digit and at camelCase humps, and drops a plural "s", so
// "getJiraIssues" and "jira issue" share terms.
func searchTerms(s string) []string {
	var spaced strings.Builder
	prev := ' '
	for _, r := range s {
		if unicode.IsUpper(r) && unicode.IsLower(prev) {
			spaced.WriteRune(' ')
		}
		spaced.WriteRune(r)
		prev = r
	}
	terms := strings.FieldsFunc(strings.ToLower(spaced.String()), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for i, term := range terms {
		if len(term) > 3 && strings.HasSuffix(term, "s") && !strings.HasSuffix(term, "ss") {
			terms[i] = strings.TrimSuffix(term, "s")
		}
	}
	return terms
}

func matchesAny(query, terms []string) bool {
	for _, term := range query {
		if slices.Contains(terms, term) {
			return true
		}
	}
	return false
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	const maxRunes = 200
	if runes := []rune(s); len(runes) > maxRunes {
		s = string(runes[:maxRunes]) + "..."
	}
	return s
}

// instructionSchemas picks the tools whose schemas follow a server's
// instructions in a search result: tools the instructions name, in order
// of first mention, that have required fields (the others can't fail
// validation by omission). At most two are taken, within 4 KB of compact
// schema JSON in total; a schema that would overflow is skipped.
func instructionSchemas(instructions string, tools []CatalogueTool) []CatalogueTool {
	type mention struct {
		tool CatalogueTool
		at   int
	}
	var mentions []mention
	for _, tool := range tools {
		at := mentionIndex(instructions, tool.MCP.MCPToolName())
		if at >= 0 && len(tool.MCP.Info().Required) > 0 {
			mentions = append(mentions, mention{tool: tool, at: at})
		}
	}
	slices.SortFunc(mentions, func(a, b mention) int { return a.at - b.at })
	var picked []CatalogueTool
	remaining := maxInstructionSchemaSize
	for _, m := range mentions {
		if len(picked) == maxInstructionSchemas {
			return picked
		}
		schemaSize := len(compactSchema(m.tool.MCP))
		if schemaSize > remaining {
			continue
		}
		remaining -= schemaSize
		picked = append(picked, m.tool)
	}
	return picked
}

// mentionIndex returns where text first names tool as a whole word, or -1.
func mentionIndex(text, tool string) int {
	isNamePart := func(r byte) bool {
		return r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
	}
	for from := 0; ; {
		i := strings.Index(text[from:], tool)
		if i < 0 {
			return -1
		}
		start, end := from+i, from+i+len(tool)
		if (start == 0 || !isNamePart(text[start-1])) && (end == len(text) || !isNamePart(text[end])) {
			return start
		}
		from = end
	}
}

func compactSchema(tool *Tool) string {
	raw, err := json.Marshal(tool.InputSchema())
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// findTool matches name against the server's tools by raw MCP name, by
// the <server>/<tool> form a server-less search prints, or by the full
// mcp_<server>_<tool> name, in any case: models echo whichever form they
// last saw. An exact raw name wins over a case-fold match.
func findTool(server, name string, tools []CatalogueTool) (CatalogueTool, bool) {
	for _, tool := range tools {
		if tool.MCP.MCPToolName() == name {
			return tool, true
		}
	}
	lower := strings.TrimPrefix(strings.ToLower(name), strings.ToLower(server)+"/")
	for _, tool := range tools {
		raw := strings.ToLower(tool.MCP.MCPToolName())
		if lower == raw || lower == strings.ToLower("mcp_"+server+"_")+raw {
			return tool, true
		}
	}
	return CatalogueTool{}, false
}

type describeParams struct {
	Server string   `json:"server"`
	Tools  []string `json:"tools"`
}

func (c *catalogue) describe(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params describeParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid input: " + err.Error()), nil
	}
	params.Server = c.canonicalServer(params.Server)
	if len(params.Tools) == 0 {
		return fantasy.NewTextErrorResponse("tools is empty: name up to 10 tools, as search_mcp_tools returns them"), nil
	}
	_, tools, err := c.serverTools(ctx, params.Server, DescribeMCPToolToolName)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	var out strings.Builder
	for _, name := range params.Tools[:min(len(params.Tools), maxDescribedTools)] {
		tool, ok := findTool(params.Server, name, tools)
		if !ok {
			fmt.Fprintf(&out, "## %s\nNo such tool on %s.\n\n", name, params.Server)
			continue
		}
		fmt.Fprintf(&out, "## %s\n%s\n\nInput schema:\n%s\n\n", tool.MCP.MCPToolName(), tool.MCP.tool.Description, compactSchema(tool.MCP))
	}
	if len(params.Tools) > maxDescribedTools {
		fmt.Fprintf(&out, "Not described (limit %d): %s\n", maxDescribedTools, strings.Join(params.Tools[maxDescribedTools:], ", "))
	}
	return fantasy.NewTextResponse(out.String()), nil
}

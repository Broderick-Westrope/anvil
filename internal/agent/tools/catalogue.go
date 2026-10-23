package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"text/template"
	"unicode"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/google/jsonschema-go/jsonschema"
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
			Required: []string{"server", "tool", "arguments"},
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

// errUnusable carries a non-error explanation of why a server can't be
// used right now: the call was well formed, so it isn't the model's
// mistake to retry.
type errUnusable struct{ msg string }

func (e errUnusable) Error() string { return e.msg }

// serverTools resolves server for this agent: it checks the agent may use
// it, connects it if deferred or waits if it is starting, and returns the
// server's instructions and the agent's tools on it, in name order.
func (c *catalogue) serverTools(ctx context.Context, server string) (string, []CatalogueTool, error) {
	if _, ok := c.descriptions[server]; !ok {
		names := make([]string, 0, len(c.opts.Servers))
		for _, s := range c.opts.Servers {
			names = append(names, s.Name)
		}
		return "", nil, fmt.Errorf("unknown catalogue server %q, available servers: %s", server, strings.Join(names, ", "))
	}
	instructions, err := c.ready(ctx, server)
	if err != nil {
		return "", nil, err
	}
	tools := c.opts.Tools(server)
	slices.SortFunc(tools, func(a, b CatalogueTool) int {
		return strings.Compare(a.MCP.MCPToolName(), b.MCP.MCPToolName())
	})
	return instructions, tools, nil
}

// ready brings server to a usable state, following enable_mcp's handling
// of each connection state, and returns its instructions.
func (c *catalogue) ready(ctx context.Context, server string) (string, error) {
	// A deferred server takes one connect, and a starting one one wait;
	// three passes cover a connect that finds another caller connecting.
	for range 3 {
		info, ok := mcp.GetState(server)
		if !ok {
			return "", errUnusable{fmt.Sprintf("MCP '%s' isn't running in this process", server)}
		}
		switch info.State {
		case mcp.StateConnected, mcp.StateLazy:
			if info.Client == nil {
				return "", nil
			}
			return info.Client.InitializeResult().Instructions, nil
		case mcp.StateDisabled:
			return "", errUnusable{fmt.Sprintf("MCP '%s' is disabled. Tell the user if the task needs it.", server)}
		case mcp.StateError:
			return "", stateErrorProblem(server, info)
		case mcp.StateStarting:
			if err := c.settle(ctx, server); err != nil {
				return "", err
			}
		case mcp.StateDeferred:
			if err := c.connect(ctx, server); err != nil {
				return "", err
			}
		}
	}
	return "", errUnusable{fmt.Sprintf("MCP '%s' is still starting, retry shortly", server)}
}

func stateErrorProblem(server string, info mcp.ClientInfo) error {
	if info.NeedsAuth {
		return errUnusable{authRequiredResponse(server, SearchMCPToolsToolName).Content}
	}
	reason := "unknown error"
	if info.Error != nil {
		reason = info.Error.Error()
	}
	return errUnusable{fmt.Sprintf("MCP '%s' failed to connect: %s", server, reason)}
}

func (c *catalogue) connect(ctx context.Context, server string) error {
	err := mcp.ConnectDeferred(ctx, server, c.opts.Config)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mcp.ErrAlreadyConnecting):
		return c.settle(ctx, server)
	case mcp.NeedsAuth(err):
		return errUnusable{authRequiredResponse(server, SearchMCPToolsToolName).Content}
	default:
		return errUnusable{fmt.Sprintf("Failed to connect MCP '%s': %s", server, err)}
	}
}

// settle waits, bounded by the server's connect timeout, for another
// caller's connect to finish.
func (c *catalogue) settle(ctx context.Context, server string) error {
	ctx, cancel := context.WithTimeout(ctx, mcp.ConnectTimeout(c.opts.Config.Config().MCP[server]))
	defer cancel()
	if _, err := mcp.WaitSettled(ctx, server); err != nil {
		if parent := context.Cause(ctx); parent != nil && !errors.Is(parent, context.DeadlineExceeded) {
			return parent
		}
		return errUnusable{fmt.Sprintf("MCP '%s' is still starting, retry shortly", server)}
	}
	return nil
}

// problemResponse turns a resolve error into a tool response: a server
// that can't be used now is reported as a result, anything else as an
// error.
func problemResponse(err error) fantasy.ToolResponse {
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
	query := searchTerms(params.Query)
	if params.Server == "" {
		return fantasy.NewTextResponse(c.searchConnected(ctx, query)), nil
	}
	instructions, tools, err := c.serverTools(ctx, params.Server)
	if err != nil {
		return problemResponse(err), nil
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
		if info.State != mcp.StateConnected && info.State != mcp.StateLazy {
			if matchesAny(query, searchTerms(server.Name+" "+server.Description)) {
				fmt.Fprintf(&out, "%s (%s) isn't connected yet; pass server=%q to search it.\n", server.Name, server.Description, server.Name)
			}
			continue
		}
		_, tools, err := c.serverTools(ctx, server.Name)
		if err != nil {
			continue
		}
		byServer[server.Name] = tools
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

func searchTerms(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
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
	const maxLen = 200
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
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
	size := 0
	for _, m := range mentions {
		if len(picked) == maxInstructionSchemas {
			break
		}
		schemaSize := len(compactSchema(m.tool.MCP))
		if size+schemaSize > maxInstructionSchemaSize {
			continue
		}
		size += schemaSize
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

// findTool matches name against the server's tools by raw MCP name or by
// the full mcp_<server>_<tool> name, in any case: models echo whichever
// form they last saw.
func findTool(server, name string, tools []CatalogueTool) (CatalogueTool, bool) {
	lower := strings.ToLower(name)
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
	_, tools, err := c.serverTools(ctx, params.Server)
	if err != nil {
		return problemResponse(err), nil
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
	return fantasy.NewTextResponse(out.String()), nil
}

type callParams struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c *catalogue) call(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var params callParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return fantasy.NewTextErrorResponse("invalid input: " + err.Error()), nil
	}
	instructions, tools, err := c.serverTools(ctx, params.Server)
	if err != nil {
		return problemResponse(err), nil
	}
	tool, ok := findTool(params.Server, params.Tool, tools)
	if !ok {
		return fantasy.NewTextErrorResponse(unknownToolMessage(params.Server, params.Tool, tools)), nil
	}
	args, problem := callArguments(params.Arguments)
	if problem == "" {
		problem = validateArguments(tool.MCP, args)
	}
	if problem != "" {
		marker := ""
		if mentionIndex(instructions, tool.MCP.MCPToolName()) >= 0 {
			marker = InstructionNamedMarker
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("%s%s: %s\n\nInput schema for %s:\n%s",
			InvalidArgumentsPrefix, marker, problem, tool.MCP.MCPToolName(), compactSchema(tool.MCP))), nil
	}
	return tool.Run.Run(ctx, fantasy.ToolCall{ID: call.ID, Name: tool.Run.Info().Name, Input: string(args)})
}

func unknownToolMessage(server, name string, tools []CatalogueTool) string {
	matches := rankTools(searchTerms(name), map[string][]CatalogueTool{server: tools})
	if len(matches) == 0 {
		return fmt.Sprintf("no tool %q on %s. Use search_mcp_tools to find it.", name, server)
	}
	names := make([]string, 0, maxClosestMatches)
	for _, match := range matches[:min(len(matches), maxClosestMatches)] {
		names = append(names, match.tool.MCPToolName())
	}
	return fmt.Sprintf("no tool %q on %s. Closest matches: %s", name, server, strings.Join(names, ", "))
}

// callArguments accepts a JSON object or a string holding one, since
// models often send nested objects as strings. Missing arguments are an
// empty object.
func callArguments(raw json.RawMessage) ([]byte, string) {
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		raw = json.RawMessage(asString)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return []byte("{}"), ""
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, "arguments must be a JSON object"
	}
	return raw, ""
}

// validateArguments checks args against the tool's input schema and
// describes any violations. A schema jsonschema-go can't load (it won't
// unmarshal, or has a remote $ref) passes every call through.
func validateArguments(tool *Tool, args []byte) string {
	raw, err := json.Marshal(tool.InputSchema())
	if err != nil {
		return ""
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		slog.Debug("MCP tool schema won't load; passing call through unvalidated", "tool", tool.Name(), "error", err)
		return ""
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		slog.Debug("MCP tool schema won't resolve; passing call through unvalidated", "tool", tool.Name(), "error", err)
		return ""
	}
	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		return "arguments are not valid JSON: " + err.Error()
	}
	if err := resolved.Validate(value); err != nil {
		return err.Error()
	}
	return ""
}

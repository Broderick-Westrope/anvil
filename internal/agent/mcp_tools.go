package agent

import (
	"maps"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/hooks"
)

// mcpListing is what building an agent's tool list decided about MCP
// servers, for its turns to apply.
type mcpListing struct {
	// catalogue holds the catalogue-routed servers. Calls to their tools
	// by direct name are forwarded to call_mcp_tool.
	catalogue map[string]bool
	// named holds the catalogue servers with a tool listed because a
	// workflow agent named it. That agent never searches, so their
	// instructions go in its system prompt.
	named map[string]bool
	// routed lists every server in the route map, in name order, so a
	// direct-name call can be matched to its server.
	routed []string
}

// mcpInstructions joins the instructions of connected MCP servers in name
// order, leaving out catalogue servers (search results carry theirs). The
// order is fixed so the system prompt is identical across runs and its
// prompt cache stays valid.
func mcpInstructions(listing mcpListing) string {
	states := toolsmcp.GetStates()
	var instructions strings.Builder
	for _, name := range slices.Sorted(maps.Keys(states)) {
		server := states[name]
		if server.State != toolsmcp.StateConnected {
			continue
		}
		if listing.catalogue[name] && !listing.named[name] {
			continue
		}
		if s := server.Client.InitializeResult().Instructions; s != "" {
			instructions.WriteString(s)
			instructions.WriteString("\n\n")
		}
	}
	return instructions.String()
}

// agentMCPTools builds one agent's MCP tools. The tool list and the
// catalogue resolver both go through it, so the rules for which tools an
// agent may use and how they are wrapped can't drift apart.
type agentMCPTools struct {
	c          *coordinator
	access     toolAccess
	hookRunner *hooks.Runner
	isSubAgent bool
	// routes is nil until the startup snapshot exists. Until then no MCP
	// or catalogue tools are built, as no server has connected yet; every
	// run waits for the snapshot and rebuilds before its first request.
	routes mcpRoutes
}

func (c *coordinator) newAgentMCPTools(access toolAccess, hookRunner *hooks.Runner, isSubAgent bool) agentMCPTools {
	routes, _ := c.mcpRoutes()
	return agentMCPTools{c: c, access: access, hookRunner: hookRunner, isSubAgent: isSubAgent, routes: routes}
}

// allowed returns the agent's unwrapped MCP tools: every registered tool
// its allowed_mcp permits, sorted by name.
func (m agentMCPTools) allowed() []*tools.Tool {
	var out []*tools.Tool
	for _, tool := range tools.GetMCPTools(m.c.permissions, m.c.cfg, m.c.cfg.WorkingDir()) {
		if m.access.allowsMCPTool(tool.MCP(), tool.MCPToolName()) {
			out = append(out, tool)
		}
	}
	slices.SortFunc(out, func(a, b *tools.Tool) int { return strings.Compare(a.Name(), b.Name()) })
	return out
}

// wrap applies the hook wrapper (top-level agents only) every agent tool
// gets.
func (m agentMCPTools) wrap(agentTools []fantasy.AgentTool) []fantasy.AgentTool {
	return wrapToolsWithHooks(agentTools, m.hookRunner, m.isSubAgent)
}

// listed returns the MCP tools that go in the agent's tool list, unwrapped:
// those of servers not routed through the catalogue, plus any tool named
// in named (a workflow agent's explicit tool list), whatever its route.
func (m agentMCPTools) listed(named []string) ([]fantasy.AgentTool, mcpListing) {
	listing := mcpListing{catalogue: map[string]bool{}, named: map[string]bool{}}
	if m.routes == nil {
		return nil, listing
	}
	for _, server := range m.routes.catalogueServers() {
		listing.catalogue[server] = true
	}
	listing.routed = slices.Sorted(maps.Keys(m.routes))
	var out []fantasy.AgentTool
	for _, tool := range m.allowed() {
		if m.routes.isCatalogue(tool.MCP()) {
			if !slices.Contains(named, tool.Name()) {
				continue
			}
			listing.named[tool.MCP()] = true
		}
		out = append(out, tool)
	}
	return out, listing
}

// catalogueTools returns the catalogue tools when at least one
// catalogue-routed server passes the agent's allowed_mcp, else nil. The
// server list comes from the route map alone, so it is fixed for the
// process.
func (m agentMCPTools) catalogueTools() []fantasy.AgentTool {
	var servers []tools.CatalogueServer
	for _, name := range m.routes.catalogueServers() {
		if m.access.allowsMCPServer(name) {
			servers = append(servers, tools.CatalogueServer{Name: name, Description: m.routes[name].description})
		}
	}
	if len(servers) == 0 {
		return nil
	}
	var allowed []fantasy.AgentTool
	for _, tool := range tools.NewCatalogueTools(tools.CatalogueOptions{Servers: servers, Tools: m.resolve, Config: m.c.cfg}) {
		if m.access.allowsCatalogueTool(tool.Info().Name) {
			allowed = append(allowed, tool)
		}
	}
	return allowed
}

// resolve returns the agent's tools on one server, wrapped as they would
// be in the tool list. It matches tools by their server, never by parsing
// names: server names can contain "_" and the OAuth rename changes case.
func (m agentMCPTools) resolve(server string) []tools.CatalogueTool {
	var out []tools.CatalogueTool
	for _, tool := range m.allowed() {
		if tool.MCP() == server {
			out = append(out, tools.CatalogueTool{MCP: tool, Run: m.wrap([]fantasy.AgentTool{tool})[0]})
		}
	}
	return out
}

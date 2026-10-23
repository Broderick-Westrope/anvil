package agent

import (
	"slices"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/hooks"
)

// mcpListing is what building an agent's tool list decided about MCP
// servers, for its turns to apply.
type mcpListing struct {
	// lazyTools maps each listed tool of a lazy server to the server, so
	// turns can hide it until the server is enabled.
	lazyTools map[string]string
	// catalogue holds the catalogue-routed servers none of whose tools
	// are listed. Their instructions stay out of the system prompt and
	// replayed enables of them are ignored.
	catalogue map[string]bool
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
// its allowed_mcp permits, in no particular order.
func (m agentMCPTools) allowed() []*tools.Tool {
	var out []*tools.Tool
	for _, tool := range tools.GetMCPTools(m.c.permissions, m.c.cfg, m.c.cfg.WorkingDir()) {
		if m.access.allowsMCPTool(tool.MCP(), tool.MCPToolName()) {
			out = append(out, tool)
		}
	}
	return out
}

// wrap applies the hook (top-level agents only) and error-result wrappers
// every agent tool gets.
func (m agentMCPTools) wrap(agentTools []fantasy.AgentTool) []fantasy.AgentTool {
	return wrapToolsWithErrorResults(wrapToolsWithHooks(agentTools, m.hookRunner, m.isSubAgent))
}

// listed returns the MCP tools that go in the agent's tool list, unwrapped:
// those of servers not routed through the catalogue, plus any tool named
// in named (a workflow agent's explicit tool list), whatever its route.
func (m agentMCPTools) listed(named []string) ([]fantasy.AgentTool, mcpListing) {
	listing := mcpListing{lazyTools: map[string]string{}, catalogue: map[string]bool{}}
	if m.routes == nil {
		return nil, listing
	}
	for _, server := range m.routes.catalogueServers() {
		listing.catalogue[server] = true
	}
	cfg := m.c.cfg.Config()
	var out []fantasy.AgentTool
	for _, tool := range m.allowed() {
		isNamed := slices.Contains(named, tool.Name())
		if m.routes.isCatalogue(tool.MCP()) && !isNamed {
			continue
		}
		if isNamed {
			delete(listing.catalogue, tool.MCP())
		}
		out = append(out, tool)
		if mcpCfg, ok := cfg.MCP[tool.MCP()]; ok && mcpCfg.IsLazy() {
			listing.lazyTools[tool.Name()] = tool.MCP()
		}
	}
	return out, listing
}

// enableMCPTool returns enable_mcp for the lazy servers the agent may use
// that aren't routed through the catalogue, or nil when there are none.
func (m agentMCPTools) enableMCPTool(connect tools.ConnectFn) fantasy.AgentTool {
	if m.routes == nil {
		return nil
	}
	lazyMCPs := map[string]string{}
	for name, mcpCfg := range m.c.cfg.Config().MCP {
		if mcpCfg.IsLazy() && !m.routes.isCatalogue(name) && m.access.allowsMCPServer(name) {
			lazyMCPs[name] = mcpCfg.LazyDescription
		}
	}
	if len(lazyMCPs) == 0 {
		return nil
	}
	return tools.NewEnableMCPTool(lazyMCPs, connect)
}

// catalogueTools returns the catalogue tools when at least one
// catalogue-routed server passes the agent's allowed_mcp, else nil. The
// server list comes from the route map alone, so it is fixed for the
// process.
func (m agentMCPTools) catalogueTools(cfg *config.ConfigStore) []fantasy.AgentTool {
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
	for _, tool := range tools.NewCatalogueTools(tools.CatalogueOptions{Servers: servers, Tools: m.resolve, Config: cfg}) {
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

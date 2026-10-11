package agent

import (
	"slices"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
)

// toolAccess decides which tools an agent may use. Built-in tools follow
// the agent's AllowedTools filter and the global DisabledTools list; MCP
// tools and lazy MCPs follow the agent's AllowedMCP map.
type toolAccess struct {
	allowedTools  []string
	disabledTools []string
	allowedMCP    map[string][]string
}

// newToolAccess returns the access rules for agent. An invalid
// AllowedTools filter allows every built-in tool, and the error says why.
func newToolAccess(agent config.Agent, opts *config.Options) (toolAccess, error) {
	access := toolAccess{allowedTools: agent.AllowedTools, allowedMCP: agent.AllowedMCP}
	if opts != nil {
		access.disabledTools = opts.DisabledTools
	}
	err := config.ValidateFilterList(agent.AllowedTools)
	if err != nil {
		access.allowedTools = nil
	}
	return access, err
}

// allowsTool reports whether the agent may use the built-in tool name. The
// job tools come with bash, because bash can move any command to the
// background, unless the filter excludes them by name ("!job_kill").
func (a toolAccess) allowsTool(name string) bool {
	if slices.Contains(a.disabledTools, name) {
		return false
	}
	if config.FilterAllows(a.allowedTools, name) {
		return true
	}
	return slices.Contains(tools.JobToolNames(), name) &&
		config.FilterAllows(a.allowedTools, tools.BashToolName) &&
		!slices.Contains(a.allowedTools, "!"+name)
}

// allowsMCPTool reports whether the agent may use the tool named tool from
// the MCP server. A nil AllowedMCP allows everything; otherwise the server
// must be listed, with either no tools (all of them) or this tool.
func (a toolAccess) allowsMCPTool(server, tool string) bool {
	if a.allowedMCP == nil {
		return true
	}
	serverTools, ok := a.allowedMCP[server]
	return ok && (len(serverTools) == 0 || slices.Contains(serverTools, tool))
}

// allowsMCPServer reports whether the agent may enable the lazy MCP
// server: AllowedMCP is nil or lists the server.
func (a toolAccess) allowsMCPServer(server string) bool {
	if a.allowedMCP == nil {
		return true
	}
	_, ok := a.allowedMCP[server]
	return ok
}

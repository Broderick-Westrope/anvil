package agent

import (
	"encoding/json"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

// deriveLazyMCPState scans messages chronologically and returns the set
// of lazy MCP servers that are currently enabled. It inspects:
//   - ToolCall parts with name == enable_mcp → server enabled
//   - MCPToggleContent parts → server enabled/disabled per the Enabled field
//
// The last event per server wins.
func deriveLazyMCPState(messages []message.Message) map[string]bool {
	enabled := make(map[string]bool)
	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case message.ToolCall:
				if p.Name != tools.EnableMCPToolName {
					continue
				}
				var params tools.EnableMCPParams
				if err := json.Unmarshal([]byte(p.Input), &params); err == nil && params.ServerName != "" {
					enabled[params.ServerName] = true
				}
			case message.MCPToggleContent:
				enabled[p.ServerName] = p.Enabled
			}
		}
	}
	return enabled
}

// filterAllowedLazyMCPs applies the agent's AllowedMCP restrictions to the
// set of lazy MCPs discovered from config. The rules are:
//   - allowedMCP == nil → no restrictions, keep all lazy MCPs
//   - len(allowedMCP) == 0 → remove all lazy MCPs
//   - otherwise → keep only lazy MCPs whose name is a key in allowedMCP
func filterAllowedLazyMCPs(lazyMCPs map[string]string, allowedMCP map[string][]string) map[string]string {
	if allowedMCP == nil {
		return lazyMCPs
	}
	if len(allowedMCP) == 0 {
		return nil
	}
	filtered := make(map[string]string, len(lazyMCPs))
	for name, desc := range lazyMCPs {
		if _, ok := allowedMCP[name]; ok {
			filtered[name] = desc
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// filterLazyMCPTools returns a subset of agentTools that excludes tools
// belonging to lazy MCP servers unless the server is enabled in the given
// LazyMCPState. Tools not present in lazyMCPToolMap pass through unchanged.
func filterLazyMCPTools(
	agentTools []fantasy.AgentTool,
	lazyMCPToolMap map[string]string,
	state *tools.LazyMCPState,
) []fantasy.AgentTool {
	if len(lazyMCPToolMap) == 0 {
		return agentTools
	}
	filtered := make([]fantasy.AgentTool, 0, len(agentTools))
	for _, t := range agentTools {
		serverName, isLazy := lazyMCPToolMap[t.Info().Name]
		if !isLazy || (state != nil && state.IsEnabled(serverName)) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// lazyServerNames returns the unique set of server names present in the
// lazyMCPToolMap.
func lazyServerNames(lazyMCPToolMap map[string]string) map[string]bool {
	servers := make(map[string]bool, len(lazyMCPToolMap))
	for _, serverName := range lazyMCPToolMap {
		servers[serverName] = true
	}
	return servers
}

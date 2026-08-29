package agent

import (
	"encoding/json"
	"log/slog"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

// deriveLazyMCPState scans messages chronologically and returns the set
// of lazy MCP servers that are currently enabled. It inspects:
//   - ToolCall parts with name == enable_mcp, correlated with their
//     ToolResult via ToolCallID: counted as enabled only when the result
//     exists and IsError == false. Missing results are treated as not
//     enabled (e.g. interrupted run).
//   - MCPToggleContent parts → server enabled/disabled per the Enabled
//     field. These have no ToolResult and are always honoured because
//     the palette records them only after a successful connect.
//
// The last event per server wins, respecting chronological order.
func deriveLazyMCPState(messages []message.Message) map[string]bool {
	// Collect all events in order and build a result index in a single pass.
	type event struct {
		serverName string
		callID     string // Non-empty for enable_mcp ToolCalls.
		enabled    bool   // For MCPToggleContent: the toggle value.
		isToggle   bool   // True for MCPToggleContent events.
	}
	var events []event
	resultOK := make(map[string]bool) // callID → true if result exists and not error.

	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case message.ToolCall:
				if p.Name != tools.EnableMCPToolName {
					continue
				}
				var params tools.EnableMCPParams
				if err := json.Unmarshal([]byte(p.Input), &params); err != nil {
					slog.Debug("Failed to parse enable_mcp input", "error", err, "input", p.Input)
					continue
				}
				if params.ServerName != "" && p.ID != "" {
					events = append(events, event{serverName: params.ServerName, callID: p.ID})
				}
			case message.ToolResult:
				if p.ToolCallID != "" {
					resultOK[p.ToolCallID] = !p.IsError
				}
			case message.MCPToggleContent:
				events = append(events, event{
					serverName: p.ServerName,
					enabled:    p.Enabled,
					isToggle:   true,
				})
			}
		}
	}

	// Apply events in order. enable_mcp calls are resolved against
	// the result index; MCPToggleContent entries apply directly.
	enabled := make(map[string]bool)
	for _, e := range events {
		if e.isToggle {
			enabled[e.serverName] = e.enabled
		} else {
			ok, found := resultOK[e.callID]
			if found && ok {
				enabled[e.serverName] = true
			}
			// Missing result or IsError: do not mark enabled.
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
//
// Stale-snapshot invariant: lazyMCPToolMap is a Run-start snapshot while
// agentTools is live (updated by SetTools when a deferred server
// connects mid-run). Newly registered tools are absent from the stale
// snapshot, so they pass through unfiltered — correct because the
// server was just explicitly enabled.
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

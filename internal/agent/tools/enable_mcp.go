package tools

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
)

// EnableMCPToolName is the tool name for the enable_mcp tool.
const EnableMCPToolName = "enable_mcp"

//go:embed enable_mcp.md.tpl
var enableMCPDescriptionTmpl []byte

var enableMCPDescriptionTpl = template.Must(
	template.New("enableMCPDescription").
		Parse(string(enableMCPDescriptionTmpl)),
)

type lazyMCPEntry struct {
	Name        string
	Description string
}

type enableMCPDescriptionData struct {
	LazyMCPs []lazyMCPEntry
}

// EnableMCPParams contains the parameters for the enable_mcp tool.
type EnableMCPParams struct {
	ServerName string `json:"server_name" description:"The exact name of the MCP server to enable"`
}

// ConnectFn connects a deferred MCP server and rebuilds tools. It returns
// the number of tools registered on success.
type ConnectFn func(ctx context.Context, name string) (toolCount int, err error)

// NewEnableMCPTool creates the enable_mcp tool. The lazyMCPs argument
// maps server name to its lazy description. connectFn is an optional
// callback that connects deferred servers and rebuilds the agent's tool
// list; when nil, deferred servers cannot be connected (the tool returns
// an error for deferred servers).
func NewEnableMCPTool(lazyMCPs map[string]string, connectFn ConnectFn) fantasy.AgentTool {
	entries := make([]lazyMCPEntry, 0, len(lazyMCPs))
	for name, desc := range lazyMCPs {
		entries = append(entries, lazyMCPEntry{Name: name, Description: desc})
	}
	slices.SortFunc(entries, func(a, b lazyMCPEntry) int {
		return strings.Compare(a.Name, b.Name)
	})

	description := renderTemplate(enableMCPDescriptionTpl, enableMCPDescriptionData{
		LazyMCPs: entries,
	})

	return fantasy.NewAgentTool(
		EnableMCPToolName,
		description,
		func(ctx context.Context, params EnableMCPParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ServerName == "" {
				return fantasy.NewTextErrorResponse("server_name is required"), nil
			}

			// Validate server name exists in the lazy MCPs map.
			if _, ok := lazyMCPs[params.ServerName]; !ok {
				available := make([]string, 0, len(lazyMCPs))
				for name := range lazyMCPs {
					available = append(available, name)
				}
				slices.Sort(available)
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("unknown server %q, available servers: %s",
						params.ServerName, strings.Join(available, ", ")),
				), nil
			}

			// Get LazyMCPState from context.
			state := GetLazyMCPState(ctx)
			if state == nil {
				return fantasy.NewTextErrorResponse("lazy MCP state not initialized"), nil
			}

			// Check MCP connection state.
			info, exists := mcp.GetState(params.ServerName)
			if exists {
				switch info.State {
				case mcp.StateDeferred:
					// Deferred server: connect on first enable.
					return enableDeferred(ctx, params.ServerName, state, connectFn)
				case mcp.StateError:
					if info.NeedsAuth {
						return authRequiredResponse(params.ServerName), nil
					}
					errMsg := "unknown error"
					if info.Error != nil {
						errMsg = info.Error.Error()
					}
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("MCP '%s' failed to connect: %s", params.ServerName, errMsg),
					), nil
				case mcp.StateStarting:
					return fantasy.NewTextResponse(
						fmt.Sprintf("MCP '%s' is still starting, retry shortly", params.ServerName),
					), nil
				case mcp.StateLazy, mcp.StateConnected:
					// Proceed to enable.
				}
			}

			// Enable the server (already connected).
			if state.Enable(params.ServerName) {
				return fantasy.NewTextResponse(
					fmt.Sprintf("%s MCP is already enabled", params.ServerName),
				), nil
			}

			// Count tools for this server.
			toolCount := 0
			for mcpName, tools := range mcp.Tools() {
				if mcpName == params.ServerName {
					toolCount = len(tools)
					break
				}
			}

			return fantasy.NewTextResponse(
				fmt.Sprintf("Enabled %s MCP (%d tools available)", params.ServerName, toolCount),
			), nil
		},
	)
}

// authRequiredResponse builds the tool response for a server whose
// OAuth token must be refreshed by a human. It is deliberately a
// success response rather than an error: the call did not fail
// because of a malformed request, and framing it as an error pushes
// models toward retry loops. The wording tells the model not to
// retry within the turn and not to attempt a shell command.
func authRequiredResponse(name string) fantasy.ToolResponse {
	msg := fmt.Sprintf(
		"MCP '%s' cannot be enabled: its authentication has expired "+
			"and only a human can renew it. The user has been notified "+
			"in the UI. Do not retry enable_mcp for '%s' this turn and "+
			"do not attempt to authenticate yourself. Tell the user "+
			"that '%s' needs re-authentication, then continue with "+
			"whatever part of the task does not need it."+
			" (If this is a non-interactive session, the user must run "+
			"`anvil mcp auth %s`.)",
		name, name, name, name)
	return fantasy.NewTextResponse(msg)
}

// enableDeferred handles the deferred-connect branch of enable_mcp.
// It connects the server via connectFn, records the enable in state
// only on success, and returns the tool count. On failure the state
// is left untouched so a retry next turn works.
func enableDeferred(ctx context.Context, name string, state *LazyMCPState, connectFn ConnectFn) (fantasy.ToolResponse, error) {
	if connectFn == nil {
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("MCP '%s' is deferred but no connect callback is available", name),
		), nil
	}

	toolCount, err := connectFn(ctx, name)
	if err != nil {
		if mcp.NeedsAuth(err) {
			return authRequiredResponse(name), nil
		}
		return fantasy.NewTextErrorResponse(
			fmt.Sprintf("Failed to connect MCP '%s': %s", name, err),
		), nil
	}

	// Only record the enable after a successful connect.
	state.Enable(name)

	return fantasy.NewTextResponse(
		fmt.Sprintf("Enabled %s MCP (%d tools available)", name, toolCount),
	), nil
}

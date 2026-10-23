package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// MCPToolMessageItem is a message item that represents a bash tool call.
type MCPToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*MCPToolMessageItem)(nil)

// NewMCPToolMessageItem creates a new [MCPToolMessageItem].
func NewMCPToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &MCPToolRenderContext{}, canceled)
}

// MCPToolRenderContext renders bash tool messages.
type MCPToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (b *MCPToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.ToolCall.Name == tools.CallMCPToolToolName {
		// A direct-name call to a catalogue tool streams under its own
		// name, then is renamed once it is forwarded to call_mcp_tool.
		return (&CatalogueCallRenderContext{}).RenderTool(sty, width, opts)
	}
	toolNameParts := strings.SplitN(opts.ToolCall.Name, "_", 3)
	if len(toolNameParts) != 3 {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid tool name"}, width)
	}
	return renderMCPTool(sty, width, opts, toolNameParts[1], toolNameParts[2], opts.ToolCall.Input)
}

// NewCatalogueCallMessageItem creates the item for a call_mcp_tool call,
// which renders as the MCP tool call it proxies.
func NewCatalogueCallMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &CatalogueCallRenderContext{}, canceled)
}

// CatalogueCallRenderContext renders a call_mcp_tool call as the MCP
// tool it runs: server, tool and the tool's own arguments.
type CatalogueCallRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (b *CatalogueCallRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	call, ok := parseCatalogueCall(opts.ToolCall.Input)
	if !ok {
		if opts.IsPending() {
			return pendingTool(sty, "MCP", opts.Anim, opts.Compact)
		}
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, width)
	}
	return renderMCPTool(sty, width, opts, call.Server, call.Tool, string(call.Arguments))
}

// parseCatalogueCall reads a call_mcp_tool input that names its server
// and tool.
func parseCatalogueCall(input string) (tools.CatalogueCall, bool) {
	call, err := tools.ParseCatalogueCall(input)
	return call, err == nil && call.Server != "" && call.Tool != ""
}

// expandName is the name expanded_tools patterns match a call by: a
// proxied MCP call matches as the inner tool, under the same name a
// direct call would have (including the OAuth rename).
func expandName(toolCall message.ToolCall) string {
	if toolCall.Name != tools.CallMCPToolToolName {
		return toolCall.Name
	}
	if call, ok := parseCatalogueCall(toolCall.Input); ok {
		return mcp.ToolName(call.Server, call.Tool)
	}
	return toolCall.Name
}

func renderMCPTool(sty *styles.Styles, width int, opts *ToolRenderOpts, server, tool, input string) string {
	mcpName := humanizedToolName(server)
	toolName := humanizedToolName(tool)

	mcpName = sty.Tool.MCPName.Render(mcpName)
	toolName = sty.Tool.MCPToolName.Render(toolName)

	name := fmt.Sprintf("%s %s %s", mcpName, sty.Tool.MCPArrow.String(), toolName)

	if opts.IsPending() {
		return pendingTool(sty, name, opts.Anim, opts.Compact)
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, width)
	}

	var toolParams []string
	if len(params) > 0 {
		parsed, _ := json.Marshal(params)
		toolParams = append(toolParams, string(parsed))
	}

	header := toolHeader(sty, opts.Status, name, width, opts, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() || opts.Result.Content == "" {
		return header
	}

	bodyWidth := width - toolBodyLeftPaddingTotal
	body := renderToolResultTextContent(sty, opts.Result.Content, toolResultContentWidths{Body: bodyWidth, Diff: width}, opts.ExpandedContent, opts.NoTruncate)
	return joinToolParts(header, body)
}

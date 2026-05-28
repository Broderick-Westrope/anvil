package chat

import (
	"encoding/json"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/fsext"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// -----------------------------------------------------------------------------
// Diagnostics Tool
// -----------------------------------------------------------------------------

// DiagnosticsToolMessageItem is a message item that represents a diagnostics tool call.
type DiagnosticsToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*DiagnosticsToolMessageItem)(nil)

// NewDiagnosticsToolMessageItem creates a new [DiagnosticsToolMessageItem].
func NewDiagnosticsToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &DiagnosticsToolRenderContext{}, canceled)
}

// DiagnosticsToolRenderContext renders diagnostics tool messages.
type DiagnosticsToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (d *DiagnosticsToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.IsPending() {
		return pendingTool(sty, "Diagnostics", opts.Anim, opts.Compact)
	}

	var params tools.DiagnosticsParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	// Show "project" if no file path, otherwise show the file path.
	mainParam := "project"
	if params.FilePath != "" {
		mainParam = fsext.PrettyPath(params.FilePath)
	}

	header := toolHeader(sty, opts.Status, "Diagnostics", width, opts.Compact, mainParam)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, width); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	bodyWidth := width - toolBodyLeftPaddingTotal
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent, opts.NoTruncate))
	return joinToolParts(header, body)
}

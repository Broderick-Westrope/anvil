package chat

import (
	"encoding/json"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/fsext"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// ReferencesToolMessageItem is a message item that represents a references tool call.
type ReferencesToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*ReferencesToolMessageItem)(nil)

// NewReferencesToolMessageItem creates a new [ReferencesToolMessageItem].
func NewReferencesToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	return newBaseToolMessageItem(sty, toolCall, result, &ReferencesToolRenderContext{}, canceled)
}

// ReferencesToolRenderContext renders references tool messages.
type ReferencesToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (r *ReferencesToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	if opts.IsPending() {
		return pendingTool(sty, "Find References", opts.Anim, opts.Compact)
	}

	var params tools.ReferencesParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	toolParams := []string{params.Symbol}
	if params.Path != "" {
		toolParams = append(toolParams, "path", fsext.PrettyPath(params.Path))
	}

	header := toolHeader(sty, opts.Status, "Find References", width, opts, toolParams...)
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

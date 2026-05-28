package chat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// Compile-time interface checks.
var (
	_ list.Item           = (*toolDetailHeaderItem)(nil)
	_ list.Item           = (*toolDetailSectionItem)(nil)
	_ list.Item           = (*toolDetailParamItem)(nil)
	_ list.Item           = (*toolDetailOutputItem)(nil)
	_ Expandable          = (*toolDetailParamItem)(nil)
	_ Expandable          = (*toolDetailOutputItem)(nil)
	_ list.MouseClickable = (*toolDetailParamItem)(nil)
	_ list.MouseClickable = (*toolDetailOutputItem)(nil)
	_ list.Focusable      = (*toolDetailHeaderItem)(nil)
	_ list.Focusable      = (*toolDetailSectionItem)(nil)
	_ list.Focusable      = (*toolDetailStaticItem)(nil)
	_ list.Focusable      = (*toolDetailParamItem)(nil)
	_ list.Focusable      = (*toolDetailOutputItem)(nil)
)

// detailRenderConfigurable groups the optional methods on a tool item that
// the drill-in detail view needs to configure before rendering. Implemented
// by baseToolMessageItem via embedding.
type detailRenderConfigurable interface {
	SetCompact(bool)
	SetExpandedContent(bool)
	SetNoTruncate(bool)
	IsCompact() bool
}

// BuildToolDetailItems returns multiple independently expandable list items
// for a tool drill-in view.
func BuildToolDetailItems(sty *styles.Styles, source ToolMessageItem) []MessageItem {
	tc := source.ToolCall()
	status := source.Status()

	var items []MessageItem

	// 1. Header.
	items = append(items, &toolDetailHeaderItem{Versioned: list.NewVersioned(),
		sty:    sty,
		source: source,
	})

	// 2. Input section divider.
	items = append(items, &toolDetailSectionItem{Versioned: list.NewVersioned(),
		sty:        sty,
		label:      "Input",
		toolCallID: tc.ID,
	})

	// 3. Parameter items.
	items = append(items, buildParamItems(sty, tc)...)

	// 4. Output section divider.
	items = append(items, &toolDetailSectionItem{Versioned: list.NewVersioned(),
		sty:        sty,
		label:      "Output",
		toolCallID: tc.ID,
	})

	// 5. Output item (or awaiting permission).
	if status == ToolStatusAwaitingPermission {
		items = append(items, &toolDetailStaticItem{Versioned: list.NewVersioned(),
			sty:        sty,
			id:         "tool-detail-output:" + tc.ID,
			content:    "Awaiting permission...",
			styleFunc:  func(s *styles.Styles) lipgloss.Style { return s.Tool.StateWaiting },
			toolCallID: tc.ID,
		})
	} else {
		items = append(items, &toolDetailOutputItem{Versioned: list.NewVersioned(),
			sty:    sty,
			source: source,
		})
	}

	return items
}

// buildParamItems parses the tool call input JSON and returns a
// toolDetailParamItem for each parameter, sorted by key.
func buildParamItems(sty *styles.Styles, tc message.ToolCall) []MessageItem {
	if tc.Input == "" {
		return []MessageItem{&toolDetailStaticItem{Versioned: list.NewVersioned(),
			sty:        sty,
			id:         "tool-detail-param:no-input:" + tc.ID,
			content:    "  (no input)",
			styleFunc:  func(s *styles.Styles) lipgloss.Style { return s.Tool.NameNested },
			toolCallID: tc.ID,
		}}
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(tc.Input), &params); err != nil {
		return []MessageItem{&toolDetailStaticItem{Versioned: list.NewVersioned(),
			sty:        sty,
			id:         "tool-detail-param:raw:" + tc.ID,
			content:    "  " + tc.Input,
			styleFunc:  func(s *styles.Styles) lipgloss.Style { return s.Tool.NameNested },
			toolCallID: tc.ID,
		}}
	}

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Try to find a file_path for syntax highlighting of code blocks.
	filePath, _ := params["file_path"].(string)

	var items []MessageItem
	for _, k := range keys {
		v := params[k]
		items = append(items, &toolDetailParamItem{Versioned: list.NewVersioned(),
			sty:        sty,
			key:        k,
			value:      v,
			filePath:   filePath,
			toolCallID: tc.ID,
		})
	}
	return items
}

// sectionHeader renders a dimmed "── Label " line padded with "─" to fill
// the width.
func sectionHeader(sty *styles.Styles, label string, width int) string {
	prefix := "── " + label + " "
	prefixWidth := lipgloss.Width(prefix)
	padding := ""
	if remaining := width - prefixWidth; remaining > 0 {
		padding = strings.Repeat("─", remaining)
	}
	return sty.Tool.NameNested.Render(prefix + padding)
}

// --- toolDetailHeaderItem ---

// toolDetailHeaderItem renders the metadata header: icon + tool display name.
type toolDetailHeaderItem struct {
	*list.Versioned
	sty          *styles.Styles
	source       ToolMessageItem
	focused      bool
	cachedRender string
	cachedWidth  int
	cachedFocus  bool
	lastStatus   ToolStatus
}

// Finished implements list.Item.
func (h *toolDetailHeaderItem) Finished() bool { return true }

// ID implements MessageItem.
func (h *toolDetailHeaderItem) ID() string {
	return "tool-detail-header:" + h.source.ToolCall().ID
}

// RawRender implements MessageItem.
func (h *toolDetailHeaderItem) RawRender(width int) string {
	return h.Render(width)
}

// Render implements list.Item.
func (h *toolDetailHeaderItem) Render(width int) string {
	status := h.source.Status()
	if h.cachedWidth == width && h.lastStatus == status && h.cachedFocus == h.focused {
		return h.cachedRender
	}

	tc := h.source.ToolCall()
	icon := toolIcon(h.sty, status)
	name := h.sty.Tool.NameNormal.Render(prettifyToolName(tc.Name))
	rendered := fmt.Sprintf("%s %s", icon, name)

	var prefix string
	if h.focused {
		prefix = h.sty.Messages.ToolCallFocused.Render()
	} else {
		prefix = h.sty.Messages.ToolCallBlurred.Render()
	}
	lines := strings.Split(rendered, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	rendered = strings.Join(lines, "\n")

	h.cachedRender = rendered
	h.cachedWidth = width
	h.cachedFocus = h.focused
	h.lastStatus = status
	return rendered
}

// SetFocused implements list.Focusable.
func (h *toolDetailHeaderItem) SetFocused(v bool) { h.focused = v }

// --- toolDetailSectionItem ---

// toolDetailSectionItem renders a section divider line.
type toolDetailSectionItem struct {
	*list.Versioned
	sty          *styles.Styles
	label        string
	toolCallID   string
	focused      bool
	cachedRender string
	cachedWidth  int
	cachedFocus  bool
}

// Finished implements list.Item.
func (s *toolDetailSectionItem) Finished() bool { return true }

// ID implements MessageItem.
func (s *toolDetailSectionItem) ID() string {
	return "tool-detail-section:" + s.label + ":" + s.toolCallID
}

// RawRender implements MessageItem.
func (s *toolDetailSectionItem) RawRender(width int) string {
	return s.Render(width)
}

// Render implements list.Item.
func (s *toolDetailSectionItem) Render(width int) string {
	if s.cachedWidth == width && s.cachedFocus == s.focused {
		return s.cachedRender
	}
	rendered := sectionHeader(s.sty, s.label, width)

	var prefix string
	if s.focused {
		prefix = s.sty.Messages.ToolCallFocused.Render()
	} else {
		prefix = s.sty.Messages.ToolCallBlurred.Render()
	}
	lines := strings.Split(rendered, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	rendered = strings.Join(lines, "\n")

	s.cachedRender = rendered
	s.cachedWidth = width
	s.cachedFocus = s.focused
	return rendered
}

// SetFocused implements list.Focusable.
func (s *toolDetailSectionItem) SetFocused(v bool) { s.focused = v }

// --- toolDetailStaticItem ---

// toolDetailStaticItem renders a static text line. Not expandable.
type toolDetailStaticItem struct {
	*list.Versioned
	sty          *styles.Styles
	id           string
	content      string
	styleFunc    func(*styles.Styles) lipgloss.Style
	toolCallID   string
	focused      bool
	cachedRender string
	cachedWidth  int
	cachedFocus  bool
}

// Finished implements list.Item.
func (s *toolDetailStaticItem) Finished() bool { return true }

// ID implements MessageItem.
func (s *toolDetailStaticItem) ID() string {
	return s.id
}

// RawRender implements MessageItem.
func (s *toolDetailStaticItem) RawRender(width int) string {
	return s.Render(width)
}

// Render implements list.Item.
func (s *toolDetailStaticItem) Render(width int) string {
	if s.cachedWidth == width && s.cachedFocus == s.focused {
		return s.cachedRender
	}
	rendered := s.styleFunc(s.sty).Render(s.content)

	var prefix string
	if s.focused {
		prefix = s.sty.Messages.ToolCallFocused.Render()
	} else {
		prefix = s.sty.Messages.ToolCallBlurred.Render()
	}
	lines := strings.Split(rendered, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	rendered = strings.Join(lines, "\n")

	s.cachedRender = rendered
	s.cachedWidth = width
	s.cachedFocus = s.focused
	return rendered
}

// SetFocused implements list.Focusable.
func (s *toolDetailStaticItem) SetFocused(v bool) { s.focused = v }

// --- toolDetailParamItem ---

// toolDetailParamItem renders a single input parameter. Single-line values
// are always inline. Multi-line values are expandable/collapsible.
type toolDetailParamItem struct {
	*list.Versioned
	sty        *styles.Styles
	key        string
	value      any
	filePath   string
	toolCallID string
	expanded   bool
	focused    bool

	cachedRender string
	cachedWidth  int
	cachedExpand bool
	cachedFocus  bool
}

// Finished implements list.Item.
func (p *toolDetailParamItem) Finished() bool { return true }

// ID implements MessageItem.
func (p *toolDetailParamItem) ID() string {
	return "tool-detail-param:" + p.key + ":" + p.toolCallID
}

// RawRender implements MessageItem.
func (p *toolDetailParamItem) RawRender(width int) string {
	return p.Render(width)
}

// isMultiLine reports whether this parameter's string value contains
// newlines.
func (p *toolDetailParamItem) isMultiLine() (string, bool) {
	val, ok := p.value.(string)
	if !ok {
		return "", false
	}
	return val, strings.Contains(val, "\n")
}

// Render implements list.Item.
func (p *toolDetailParamItem) Render(width int) string {
	if p.cachedWidth == width && p.cachedExpand == p.expanded && p.cachedFocus == p.focused && p.cachedRender != "" {
		return p.cachedRender
	}

	key := p.sty.Tool.NameNested.Render(p.key + ":")
	var rendered string

	if val, multi := p.isMultiLine(); multi {
		lineCount := strings.Count(val, "\n") + 1
		if p.expanded {
			// Show key on its own line, then the full code block.
			block := toolOutputCodeContent(
				p.sty, p.filePath, val, 0, width, true, true,
			)
			rendered = "  " + key + "\n" + block
		} else {
			// Collapsed: show key with line count hint.
			hint := p.sty.Tool.ContentTruncation.Render(
				fmt.Sprintf(assistantMessageTruncateFormat, lineCount),
			)
			rendered = fmt.Sprintf("  %s %s", key, hint)
		}
	} else {
		// Single-line or non-string value: always inline.
		var valStr string
		switch val := p.value.(type) {
		case string:
			valStr = val
		default:
			b, _ := json.Marshal(val)
			valStr = string(b)
		}

		keyWidth := lipgloss.Width(key)
		indent := 2 + keyWidth + 1
		if indent < width {
			valStr = lipgloss.NewStyle().Width(width - indent).Render(valStr)
		}
		rendered = fmt.Sprintf("  %s %s", key, valStr)
	}

	var prefix string
	if p.focused {
		prefix = p.sty.Messages.ToolCallFocused.Render()
	} else {
		prefix = p.sty.Messages.ToolCallBlurred.Render()
	}
	lines := strings.Split(rendered, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	rendered = strings.Join(lines, "\n")

	p.cachedRender = rendered
	p.cachedWidth = width
	p.cachedExpand = p.expanded
	p.cachedFocus = p.focused
	return rendered
}

// ToggleExpanded implements Expandable. Only multi-line params toggle.
func (p *toolDetailParamItem) ToggleExpanded() bool {
	if _, multi := p.isMultiLine(); !multi {
		return false
	}
	p.expanded = !p.expanded
	p.cachedRender = ""
	return p.expanded
}

// HandleMouseClick implements MouseClickable.
func (p *toolDetailParamItem) HandleMouseClick(_ ansi.MouseButton, _, _ int) bool {
	_, multi := p.isMultiLine()
	return multi
}

// SetFocused implements list.Focusable.
func (p *toolDetailParamItem) SetFocused(v bool) { p.focused = v }

// --- toolDetailOutputItem ---

// toolDetailOutputItem renders the tool output. Collapsed shows the
// truncated output; expanded shows the full output.
type toolDetailOutputItem struct {
	*list.Versioned
	sty    *styles.Styles
	source ToolMessageItem

	expanded      bool
	focused       bool
	cachedRender  string
	cachedWidth   int
	cachedExpand  bool
	cachedFocus   bool
	lastStatus    ToolStatus
	lastToolCall  message.ToolCall
	lastResultVer uint64
}

// Finished implements list.Item.
func (o *toolDetailOutputItem) Finished() bool { return true }

// ID implements MessageItem.
func (o *toolDetailOutputItem) ID() string {
	return "tool-detail-output:" + o.source.ToolCall().ID
}

// RawRender implements MessageItem.
func (o *toolDetailOutputItem) RawRender(width int) string {
	return o.Render(width)
}

// Render implements list.Item.
func (o *toolDetailOutputItem) Render(width int) string {
	status := o.source.Status()
	tc := o.source.ToolCall()
	resultVer := o.sourceResultVersion()

	if o.cachedWidth == width &&
		o.cachedExpand == o.expanded &&
		o.cachedFocus == o.focused &&
		o.lastStatus == status &&
		o.lastToolCall.Finished == tc.Finished &&
		o.lastToolCall.Input == tc.Input &&
		o.lastResultVer == resultVer &&
		o.cachedRender != "" {
		return o.cachedRender
	}

	rendered := o.renderOutput(width)

	var prefix string
	if o.focused {
		prefix = o.sty.Messages.ToolCallFocused.Render()
	} else {
		prefix = o.sty.Messages.ToolCallBlurred.Render()
	}
	lines := strings.Split(rendered, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	rendered = strings.Join(lines, "\n")

	o.cachedRender = rendered
	o.cachedWidth = width
	o.cachedExpand = o.expanded
	o.cachedFocus = o.focused
	o.lastStatus = status
	o.lastToolCall = tc
	o.lastResultVer = resultVer
	return rendered
}

// renderOutput renders the output by configuring the source item.
func (o *toolDetailOutputItem) renderOutput(width int) string {
	cfg, ok := o.source.(detailRenderConfigurable)
	if !ok {
		// Fallback for items that don't implement the configurable interface.
		output := o.source.RawRender(width)
		return stripFirstLine(output)
	}

	wasCompact := cfg.IsCompact()
	cfg.SetCompact(false)
	cfg.SetExpandedContent(o.expanded)
	// Always use NoTruncate in the drill-in so diff lines wrap instead
	// of being clipped with "…". Height truncation is controlled
	// separately by the expanded flag.
	cfg.SetNoTruncate(true)

	output := o.source.RawRender(width)

	// Restore original state.
	cfg.SetNoTruncate(false)
	cfg.SetExpandedContent(false)
	cfg.SetCompact(wasCompact)

	return stripFirstLine(output)
}

// stripFirstLine removes the first line from output (the tool header/summary).
func stripFirstLine(output string) string {
	if i := strings.Index(output, "\n"); i >= 0 {
		return output[i+1:]
	}
	return output
}

// sourceResultVersion returns a version counter that changes when the
// source item's result is updated.
func (o *toolDetailOutputItem) sourceResultVersion() uint64 {
	if r, ok := o.source.(interface{ ResultVersion() uint64 }); ok {
		return r.ResultVersion()
	}
	return 0
}

// ToggleExpanded implements Expandable.
func (o *toolDetailOutputItem) ToggleExpanded() bool {
	o.expanded = !o.expanded
	o.cachedRender = ""
	return o.expanded
}

// HandleMouseClick implements MouseClickable.
func (o *toolDetailOutputItem) HandleMouseClick(_ ansi.MouseButton, _, _ int) bool {
	return true
}

// SetFocused implements list.Focusable.
func (o *toolDetailOutputItem) SetFocused(v bool) { o.focused = v }

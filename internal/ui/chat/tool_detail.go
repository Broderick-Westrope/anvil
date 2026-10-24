package chat

import (
	"encoding/json"
	"fmt"
	"runtime"
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
	SetOneLineHeader(bool)
	IsCompact() bool
}

// BuildToolDetailItems returns multiple independently expandable list items
// for a tool drill-in view. images draws a result's image, or is nil.
func BuildToolDetailItems(sty *styles.Styles, source ToolMessageItem, images ImageRenderer) []MessageItem {
	tc := source.ToolCall()
	status := source.Status()
	file := &toolFile{source: source, images: images}

	var items []MessageItem

	// 1. Header.
	items = append(items, &toolDetailHeaderItem{
		Versioned: list.NewVersioned(),
		toolFile:  file,
		sty:       sty,
	})

	// 2. Input section divider.
	items = append(items, &toolDetailSectionItem{
		Versioned:  list.NewVersioned(),
		toolFile:   file,
		sty:        sty,
		label:      "Input",
		toolCallID: tc.ID,
	})

	// 3. Parameter items.
	items = append(items, buildParamItems(sty, tc, file)...)

	// 4. Output section divider.
	items = append(items, &toolDetailSectionItem{
		Versioned:  list.NewVersioned(),
		toolFile:   file,
		sty:        sty,
		label:      "Output",
		toolCallID: tc.ID,
	})

	// 5. Output item (or awaiting permission).
	if status == ToolStatusAwaitingPermission {
		items = append(items, &toolDetailStaticItem{
			Versioned:  list.NewVersioned(),
			toolFile:   file,
			sty:        sty,
			id:         "tool-detail-output:" + tc.ID,
			content:    "Awaiting permission...",
			styleFunc:  func(s *styles.Styles) lipgloss.Style { return s.Tool.StateWaiting },
			toolCallID: tc.ID,
		})
	} else {
		items = append(items, &toolDetailOutputItem{
			Versioned: list.NewVersioned(),
			toolFile:  file,
			sty:       sty,
		})
	}

	return items
}

// buildParamItems parses the tool call input JSON and returns a
// toolDetailParamItem for each parameter, sorted by key.
func buildParamItems(sty *styles.Styles, tc message.ToolCall, file *toolFile) []MessageItem {
	if tc.Input == "" {
		return []MessageItem{&toolDetailStaticItem{
			Versioned:  list.NewVersioned(),
			toolFile:   file,
			sty:        sty,
			id:         "tool-detail-param:no-input:" + tc.ID,
			content:    "(no input)",
			indent:     2,
			styleFunc:  func(s *styles.Styles) lipgloss.Style { return s.Tool.NameNested },
			toolCallID: tc.ID,
		}}
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(tc.Input), &params); err != nil {
		return []MessageItem{&toolDetailStaticItem{
			Versioned:  list.NewVersioned(),
			toolFile:   file,
			sty:        sty,
			id:         "tool-detail-param:raw:" + tc.ID,
			content:    tc.Input,
			indent:     2,
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
		items = append(items, &toolDetailParamItem{
			Versioned:  list.NewVersioned(),
			toolFile:   file,
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

// detailPrefix is the focus bar a drill-in item draws on each line.
func detailPrefix(sty *styles.Styles, focused bool) string {
	if focused {
		return sty.Messages.ToolCallFocused.Render()
	}
	return sty.Messages.ToolCallBlurred.Render()
}

// renderDetailPrefixed draws render's lines behind the focus bar. render is
// given the width left beside the bar, so the bar never pushes a line past
// width.
func renderDetailPrefixed(sty *styles.Styles, focused bool, width int, render func(inner int) string) string {
	prefix := detailPrefix(sty, focused)
	return fitDetailLines(prefix, render(max(1, width-lipgloss.Width(prefix))), width)
}

// fitDetailLines puts prefix before each line of rendered and cuts the line
// at width, which only shortens it when width is too narrow to hold the bar
// and an indent.
func fitDetailLines(prefix, rendered string, width int) string {
	lines := strings.Split(prefixDetailLines(prefix, rendered), "\n")
	for i, ln := range lines {
		lines[i] = ansi.Truncate(ln, width, "")
	}
	return strings.Join(lines, "\n")
}

// prefixDetailLines puts prefix before each line of rendered. An image
// goes through it uncut, since its lines carry graphics placeholders.
func prefixDetailLines(prefix, rendered string) string {
	lines := strings.Split(rendered, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// indentLines indents the first line of s by first columns and the rest by
// rest.
func indentLines(s string, first, rest int) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		n := rest
		if i == 0 {
			n = first
		}
		lines[i] = strings.Repeat(" ", n) + ln
	}
	return strings.Join(lines, "\n")
}

// --- toolDetailHeaderItem ---

// toolDetailHeaderItem renders the metadata header: icon + tool display name.
type toolDetailHeaderItem struct {
	*list.Versioned
	*toolFile
	sty          *styles.Styles
	focused      bool
	cachedRender string
	cachedWidth  int
	cachedFocus  bool
	cachedKeys   fileKeys
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
	keys := h.keys()
	if h.cachedWidth == width && h.lastStatus == status && h.cachedFocus == h.focused && h.cachedKeys == keys {
		return h.cachedRender
	}

	prefix := detailPrefix(h.sty, h.focused)
	tc := h.source.ToolCall()
	icon := toolIcon(h.sty, status)
	name := h.sty.Tool.NameNormal.Render(prettifyToolName(tc.Name))
	rendered := fmt.Sprintf("%s %s", icon, name)
	// Hints go from the right until the rest fit; the help bar still has
	// the keys.
	hints := h.fileHints(keys)
	for len(hints) > 0 && lipgloss.Width(prefix+rendered+strings.Join(hints, "")) > width {
		hints = hints[:len(hints)-1]
	}
	rendered += strings.Join(hints, "")
	rendered = ansi.Truncate(prefix+rendered, width, "…")

	h.cachedRender = rendered
	h.cachedWidth = width
	h.cachedFocus = h.focused
	h.cachedKeys = keys
	h.lastStatus = status
	return rendered
}

// fileHints are the header's hints for o, e and r, in that order.
func (h *toolDetailHeaderItem) fileHints(keys fileKeys) []string {
	key := func(k, tip string) string {
		return "  " + h.sty.Header.Keystroke.Render(k) + " " + h.sty.Header.KeystrokeTip.Render(tip)
	}
	var hints []string
	switch keys.open {
	case OpensFile:
		hints = append(hints, key("o", "open file"))
	case OpensImage:
		hints = append(hints, key("o", "open in viewer"))
	}
	if keys.file {
		hints = append(hints, key("e", "open in $EDITOR"), key("r", RevealLabel(runtime.GOOS)))
	}
	return hints
}

// SetFocused implements list.Focusable.
func (h *toolDetailHeaderItem) SetFocused(v bool) {
	if h.focused == v {
		return
	}
	h.focused = v
	h.Bump()
}

// --- toolDetailSectionItem ---

// toolDetailSectionItem renders a section divider line.
type toolDetailSectionItem struct {
	*list.Versioned
	*toolFile
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
	rendered := renderDetailPrefixed(s.sty, s.focused, width, func(inner int) string {
		return sectionHeader(s.sty, s.label, inner)
	})

	s.cachedRender = rendered
	s.cachedWidth = width
	s.cachedFocus = s.focused
	return rendered
}

// SetFocused implements list.Focusable.
func (s *toolDetailSectionItem) SetFocused(v bool) {
	if s.focused == v {
		return
	}
	s.focused = v
	s.Bump()
}

// --- toolDetailStaticItem ---

// toolDetailStaticItem renders static text, wrapped to the width and
// indented by indent columns. Not expandable.
type toolDetailStaticItem struct {
	*list.Versioned
	*toolFile
	sty          *styles.Styles
	id           string
	content      string
	indent       int
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
	rendered := renderDetailPrefixed(s.sty, s.focused, width, func(inner int) string {
		body := lipgloss.NewStyle().Width(max(1, inner-s.indent)).Render(s.styleFunc(s.sty).Render(s.content))
		return indentLines(body, s.indent, s.indent)
	})

	s.cachedRender = rendered
	s.cachedWidth = width
	s.cachedFocus = s.focused
	return rendered
}

// SetFocused implements list.Focusable.
func (s *toolDetailStaticItem) SetFocused(v bool) {
	if s.focused == v {
		return
	}
	s.focused = v
	s.Bump()
}

// --- toolDetailParamItem ---

// toolDetailParamItem renders a single input parameter. Single-line values
// are always inline. Multi-line values are expandable/collapsible.
type toolDetailParamItem struct {
	*list.Versioned
	*toolFile
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

	rendered := renderDetailPrefixed(p.sty, p.focused, width, p.renderBody)

	p.cachedRender = rendered
	p.cachedWidth = width
	p.cachedExpand = p.expanded
	p.cachedFocus = p.focused
	return rendered
}

// minParamValueWidth is the narrowest a value wraps to beside its key;
// narrower than that, it goes on the lines below the key instead.
const minParamValueWidth = 20

// renderBody renders the parameter at width, without the focus bar.
func (p *toolDetailParamItem) renderBody(width int) string {
	key := p.sty.Tool.NameNested.Render(p.key + ":")
	keyLine := indentLines(lipgloss.Wrap(key, max(1, width-2), ""), 2, 2)

	if val, multi := p.isMultiLine(); multi {
		if p.expanded {
			// Show key on its own line, then the full code block.
			return keyLine + "\n" + toolOutputCodeContent(p.sty, p.filePath, val, 0, width, true, true)
		}
		hint := p.sty.Tool.ContentTruncation.Render(
			fmt.Sprintf(assistantMessageTruncateFormat, strings.Count(val, "\n")+1),
		)
		return wrapBesideKey(keyLine, hint, width)
	}

	// Single-line or non-string value: shown in full, never collapsed.
	var valStr string
	switch val := p.value.(type) {
	case string:
		valStr = val
	default:
		b, _ := json.Marshal(val)
		valStr = string(b)
	}
	return wrapBesideKey(keyLine, valStr, width)
}

// wrapBesideKey wraps value to the right of a one-line keyLine, aligning
// its continuation lines under its first, or below keyLine, indented, when
// the room beside it is too narrow to read.
func wrapBesideKey(keyLine, value string, width int) string {
	if strings.Contains(keyLine, "\n") || width-lipgloss.Width(keyLine)-1 < minParamValueWidth {
		const indent = 4
		return keyLine + "\n" + indentLines(lipgloss.NewStyle().Width(max(1, width-indent)).Render(value), indent, indent)
	}
	indent := lipgloss.Width(keyLine) + 1
	return keyLine + indentLines(lipgloss.NewStyle().Width(width-indent).Render(value), 1, indent)
}

// ToggleExpanded implements Expandable. Only multi-line params toggle.
func (p *toolDetailParamItem) ToggleExpanded() bool {
	if _, multi := p.isMultiLine(); !multi {
		return false
	}
	p.expanded = !p.expanded
	p.cachedRender = ""
	p.Bump()
	return p.expanded
}

// HandleMouseClick implements MouseClickable.
func (p *toolDetailParamItem) HandleMouseClick(_ ansi.MouseButton, _, _ int) bool {
	_, multi := p.isMultiLine()
	return multi
}

// SetFocused implements list.Focusable.
func (p *toolDetailParamItem) SetFocused(v bool) {
	if p.focused == v {
		return
	}
	p.focused = v
	p.Bump()
}

// --- toolDetailOutputItem ---

// toolDetailOutputItem renders the tool output. Collapsed shows the
// truncated output; expanded shows the full output.
type toolDetailOutputItem struct {
	*list.Versioned
	*toolFile
	sty *styles.Styles
	// shownImage is the image result last rendered, which Y copies, or nil
	// when the output last rendered as text.
	shownImage *message.ToolResult

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
func (o *toolDetailOutputItem) Finished() bool { return o.sourceImage() == nil }

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
	o.shownImage = o.sourceImage()
	if o.shownImage != nil {
		return o.renderImage(o.shownImage, width)
	}
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

	// The source lays its output out beside the focus bar itself.
	rendered := fitDetailLines(detailPrefix(o.sty, o.focused), o.renderOutput(width), width)

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
	// The header's first line is dropped below, so any more would show
	// the input again under Output.
	cfg.SetOneLineHeader(true)

	output := o.source.RawRender(width)

	// Restore original state.
	cfg.SetOneLineHeader(false)
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
	o.Bump()
	return o.expanded
}

// HandleMouseClick implements MouseClickable.
func (o *toolDetailOutputItem) HandleMouseClick(_ ansi.MouseButton, _, _ int) bool {
	return true
}

// SetFocused implements list.Focusable.
func (o *toolDetailOutputItem) SetFocused(v bool) {
	if o.focused == v {
		return
	}
	o.focused = v
	o.Bump()
}

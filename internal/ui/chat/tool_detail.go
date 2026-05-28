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
)

var _ list.Item = (*ToolDetailItem)(nil)

// ToolDetailItem renders a full detail view for a tool call, shown when
// the user drills into a compact tool item.
type ToolDetailItem struct {
	sty              *styles.Styles
	sourceItem       ToolMessageItem
	sourceWasCompact bool
	cachedRender     string
	cachedWidth      int
	lastStatus       ToolStatus
	lastToolCall     message.ToolCall
	lastResultVer    uint64
}

// NewToolDetailItem creates a new ToolDetailItem backed by the given
// source tool item.
func NewToolDetailItem(sty *styles.Styles, source ToolMessageItem) *ToolDetailItem {
	// Track whether the source was compact so we can restore it after
	// rendering the full output.
	var wasCompact bool
	if c, ok := source.(interface{ IsCompact() bool }); ok {
		wasCompact = c.IsCompact()
	}
	return &ToolDetailItem{
		sty:              sty,
		sourceItem:       source,
		sourceWasCompact: wasCompact,
		lastStatus:       ToolStatus(-1),
	}
}

// ID returns a stable identifier for the detail item.
func (d *ToolDetailItem) ID() string {
	return "tool-detail:" + d.sourceItem.ToolCall().ID
}

// RawRender implements MessageItem.
func (d *ToolDetailItem) RawRender(width int) string {
	return d.Render(width)
}

// Render implements list.Item.
func (d *ToolDetailItem) Render(width int) string {
	status := d.sourceItem.Status()
	tc := d.sourceItem.ToolCall()

	resultVer := d.sourceResultVersion()

	// Cache check: reuse if nothing changed.
	if d.cachedWidth == width && d.lastStatus == status &&
		d.lastToolCall.Finished == tc.Finished &&
		d.lastToolCall.Input == tc.Input &&
		d.lastResultVer == resultVer {
		return d.cachedRender
	}

	var sections []string

	// Section 1: Metadata header.
	icon := toolIcon(d.sty, status)
	name := d.sty.Tool.NameNormal.Render(prettifyToolName(tc.Name))
	sections = append(sections, fmt.Sprintf("%s %s", icon, name))

	// Section 2: Input.
	sections = append(sections, d.renderInput(width))

	// Section 3: Output (skip for awaiting permission).
	if status == ToolStatusAwaitingPermission {
		sections = append(sections, d.sty.Tool.StateWaiting.Render("Awaiting permission..."))
	} else {
		sections = append(sections, d.renderOutput(width))
	}

	rendered := strings.Join(sections, "\n\n")
	d.cachedRender = rendered
	d.cachedWidth = width
	d.lastStatus = status
	d.lastToolCall = tc
	d.lastResultVer = d.sourceResultVersion()
	return rendered
}

// renderInput renders the input section with a dimmed header and sorted
// key-value pairs from the tool call's JSON input.
func (d *ToolDetailItem) renderInput(width int) string {
	header := d.sectionHeader("Input", width)

	tc := d.sourceItem.ToolCall()
	if tc.Input == "" {
		return header + "\n" + d.sty.Tool.NameNested.Render("  (no input)")
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(tc.Input), &params); err != nil {
		return header + "\n  " + d.sty.Tool.NameNested.Render(tc.Input)
	}

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var lines []string
	lines = append(lines, header)
	for _, k := range keys {
		v := params[k]
		var valStr string
		switch val := v.(type) {
		case string:
			valStr = val
		default:
			b, _ := json.Marshal(val)
			valStr = string(b)
		}
		key := d.sty.Tool.NameNested.Render(k + ":")
		lines = append(lines, fmt.Sprintf("  %s %s", key, valStr))
	}
	return strings.Join(lines, "\n")
}

// renderOutput renders the output section by temporarily un-compacting the
// source item and capturing its render.
func (d *ToolDetailItem) renderOutput(width int) string {
	header := d.sectionHeader("Output", width)

	// Temporarily set compact=false on the source item to get the full
	// expanded rendering, then restore the original state.
	if compactable, ok := d.sourceItem.(Compactable); ok {
		compactable.SetCompact(false)
	}

	output := d.sourceItem.RawRender(width)

	if compactable, ok := d.sourceItem.(Compactable); ok {
		compactable.SetCompact(d.sourceWasCompact)
	}

	return header + "\n" + output
}

// sectionHeader renders a dimmed "── Label " line padded with "─" to fill
// the width.
func (d *ToolDetailItem) sectionHeader(label string, width int) string {
	prefix := "── " + label + " "
	prefixWidth := lipgloss.Width(prefix)
	padding := ""
	if remaining := width - prefixWidth; remaining > 0 {
		padding = strings.Repeat("─", remaining)
	}
	return d.sty.Tool.NameNested.Render(prefix + padding)
}

// sourceResultVersion returns a version counter that changes when the
// source item's result is updated. Used for cache invalidation.
func (d *ToolDetailItem) sourceResultVersion() uint64 {
	if r, ok := d.sourceItem.(interface{ ResultVersion() uint64 }); ok {
		return r.ResultVersion()
	}
	return 0
}

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
	sty           *styles.Styles
	sourceItem    ToolMessageItem
	cachedRender  string
	cachedWidth   int
	lastStatus    ToolStatus
	lastToolCall  message.ToolCall
	lastResultVer uint64
}

// NewToolDetailItem creates a new ToolDetailItem backed by the given
// source tool item.
func NewToolDetailItem(sty *styles.Styles, source ToolMessageItem) *ToolDetailItem {
	return &ToolDetailItem{
		sty:        sty,
		sourceItem: source,
		lastStatus: ToolStatus(-1),
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
// key-value pairs from the tool call's JSON input. Multi-line string
// values are rendered as syntax-highlighted code blocks.
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

	// Try to find a file_path for syntax highlighting of code blocks.
	filePath, _ := params["file_path"].(string)

	var lines []string
	lines = append(lines, header)
	for _, k := range keys {
		v := params[k]
		key := d.sty.Tool.NameNested.Render(k + ":")

		switch val := v.(type) {
		case string:
			if strings.Contains(val, "\n") {
				// Multi-line string: render as a code block below the key.
				lines = append(lines, "  "+key)
				block := toolOutputCodeContent(
					d.sty, filePath, val, 0, width, true, true,
				)
				lines = append(lines, block, "")
			} else {
				// Single-line string: inline key-value.
				keyWidth := lipgloss.Width(key)
				indent := 2 + keyWidth + 1
				valStr := val
				if indent < width {
					valStr = lipgloss.NewStyle().Width(width - indent).Render(valStr)
				}
				lines = append(lines, fmt.Sprintf("  %s %s", key, valStr))
			}
		default:
			b, _ := json.Marshal(val)
			valStr := string(b)
			keyWidth := lipgloss.Width(key)
			indent := 2 + keyWidth + 1
			if indent < width {
				valStr = lipgloss.NewStyle().Width(width - indent).Render(valStr)
			}
			lines = append(lines, fmt.Sprintf("  %s %s", key, valStr))
		}
	}
	return strings.Join(lines, "\n")
}

// detailRenderConfigurable groups the optional methods on a tool item that
// the drill-in detail view needs to configure before rendering. Implemented
// by baseToolMessageItem via embedding.
type detailRenderConfigurable interface {
	SetCompact(bool)
	SetExpandedContent(bool)
	SetNoTruncate(bool)
	IsCompact() bool
}

// renderOutput renders the output section by temporarily un-compacting the
// source item and capturing its render.
func (d *ToolDetailItem) renderOutput(width int) string {
	header := d.sectionHeader("Output", width)

	// Configure the source item for full expanded, non-truncated rendering,
	// then restore original state after capturing the output.
	if cfg, ok := d.sourceItem.(detailRenderConfigurable); ok {
		wasCompact := cfg.IsCompact()
		cfg.SetCompact(false)
		cfg.SetExpandedContent(true)
		cfg.SetNoTruncate(true)

		output := d.sourceItem.RawRender(width)

		cfg.SetNoTruncate(false)
		cfg.SetExpandedContent(false)
		cfg.SetCompact(wasCompact)

		// Strip the first line (tool header/summary) since the metadata
		// section already shows this information.
		if i := strings.Index(output, "\n"); i >= 0 {
			output = output[i+1:]
		}

		return header + "\n" + output
	}

	// Fallback for items that don't implement the configurable interface.
	output := d.sourceItem.RawRender(width)
	if i := strings.Index(output, "\n"); i >= 0 {
		output = output[i+1:]
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

package dialog

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

// TreeItem represents a single node in the session tree, rendered as a
// list item with indentation and expand/collapse indicators.
type TreeItem struct {
	node           *treeNode
	label          string
	depth          int
	hasChildren    bool
	isExpanded     bool
	isLeaf         bool
	isOnActivePath bool
	t              *styles.Styles
	m              fuzzy.Match
	focused        bool
	cache          map[int]string
}

var _ list.FilterableItem = (*TreeItem)(nil)

// NewTreeItem creates a new TreeItem for display in the tree dialog.
func NewTreeItem(
	t *styles.Styles,
	node *treeNode,
	depth int,
	hasChildren bool,
	isExpanded bool,
	isLeaf bool,
	isOnActivePath bool,
	label string,
) *TreeItem {
	return &TreeItem{
		node:           node,
		label:          label,
		depth:          depth,
		hasChildren:    hasChildren,
		isExpanded:     isExpanded,
		isLeaf:         isLeaf,
		isOnActivePath: isOnActivePath,
		t:              t,
	}
}

// Filter implements [list.FilterableItem].
func (ti *TreeItem) Filter() string {
	return ti.label
}

// SetMatch implements [list.MatchSettable].
func (ti *TreeItem) SetMatch(m fuzzy.Match) {
	ti.cache = nil
	ti.m = m
}

// SetFocused implements [list.Focusable].
func (ti *TreeItem) SetFocused(focused bool) {
	if ti.focused != focused {
		ti.cache = nil
	}
	ti.focused = focused
}

// Render implements [list.Item].
func (ti *TreeItem) Render(width int) string {
	if ti.cache != nil {
		if cached, ok := ti.cache[width]; ok {
			return cached
		}
	}

	// Active path marker in a fixed-width prefix column.
	var marker string
	switch {
	case ti.isLeaf:
		marker = "● "
	case ti.isOnActivePath:
		marker = "• "
	default:
		marker = "  "
	}

	// Indentation (only present at branch points).
	indent := strings.Repeat("  ", ti.depth)

	// Expand/collapse indicator.
	var connector string
	switch {
	case ti.hasChildren && ti.isExpanded:
		connector = "▼ "
	case ti.hasChildren && !ti.isExpanded:
		connector = "▶ "
	default:
		connector = "  "
	}

	// Role indicator.
	var rolePrefix string
	switch ti.node.msg.Role {
	case message.User:
		rolePrefix = "U "
	case message.Assistant:
		rolePrefix = "A "
	default:
		rolePrefix = "  "
	}

	style := ti.t.Dialog.NormalItem
	if ti.focused {
		style = ti.t.Dialog.SelectedItem
	}

	prefix := marker + indent + connector + rolePrefix
	prefixWidth := lipgloss.Width(prefix)
	// Account for horizontal frame (padding/border/margin) that the
	// style adds so the final rendered string never exceeds width.
	frameWidth := style.GetHorizontalFrameSize()

	// Truncate the label to exactly fill remaining width.
	maxLabelWidth := max(0, width-prefixWidth-frameWidth)
	truncLabel := ansi.Truncate(ti.label, maxLabelWidth, "…")

	content := fmt.Sprintf("%s%s", prefix, truncLabel)

	result := style.Render(content)

	if ti.cache == nil {
		ti.cache = make(map[int]string)
	}
	ti.cache[width] = result
	return result
}

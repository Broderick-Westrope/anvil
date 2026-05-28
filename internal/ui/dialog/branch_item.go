package dialog

import (
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/sahilm/fuzzy"
)

// BranchItem wraps a [message.Message] to implement the [ListItem]
// interface for branch picker lists.
type BranchItem struct {
	*list.Versioned
	msg     message.Message
	t       *styles.Styles
	focused bool
	m       fuzzy.Match
	cache   map[int]string
}

// NewBranchItem creates a new BranchItem from a message and styles.
func NewBranchItem(t *styles.Styles, msg message.Message) *BranchItem {
	return &BranchItem{Versioned: list.NewVersioned(), msg: msg, t: t}
}

// Filter returns the filterable text content of the message.
func (b *BranchItem) Filter() string {
	return messageTextContent(b.msg)
}

// SetMatch sets the fuzzy match for the branch item.
func (b *BranchItem) SetMatch(m fuzzy.Match) {
	b.m = m
	b.cache = nil
	b.Bump()
}

// SetFocused sets the focus state of the branch item.
func (b *BranchItem) SetFocused(focused bool) {
	if b.focused != focused {
		b.cache = nil
		b.Bump()
	}
	b.focused = focused
}

// Render returns the string representation of the branch item.
func (b *BranchItem) Render(width int) string {
	textContent := b.Filter()
	s := ListItemStyles{
		ItemBlurred:     b.t.Dialog.NormalItem,
		ItemFocused:     b.t.Dialog.SelectedItem,
		InfoTextBlurred: b.t.Dialog.Sessions.InfoBlurred,
		InfoTextFocused: b.t.Dialog.Sessions.InfoFocused,
	}
	// Subtract the style's horizontal frame (padding) from the available
	// width so renderItem truncates content correctly before the style
	// adds its frame.
	style := s.ItemBlurred
	if b.focused {
		style = s.ItemFocused
	}
	innerWidth := max(0, width-style.GetHorizontalFrameSize())
	return renderItem(s, textContent, "", b.focused, innerWidth, b.cache, &b.m)
}

// Finished implements [list.Item]. BranchItems are static.
func (b *BranchItem) Finished() bool {
	return true
}

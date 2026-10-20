package model

import (
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// SyncAssistantSegments brings msg's nodes in line with how it should be
// split around its diagrams (see chat.NewAssistantMessageItems). It does
// nothing while the split is unchanged, so streaming updates and repeat
// updates of a finished message keep their items. It returns whether it
// replaced any.
func (m *Chat) SyncAssistantSegments(sty *styles.Styles, msg *message.Message) bool {
	idx, ok := m.idInxMap[msg.ID]
	if !ok {
		return false
	}
	// The head is the message's first node: text, or a diagram when the
	// message opens with one.
	if _, ok := m.list.ItemAt(idx).(chat.SegmentKeyed); !ok {
		return false
	}
	n := 1
	for idx+n < m.list.Len() {
		item, ok := m.list.ItemAt(idx + n).(chat.MessageItem)
		if !ok || !chat.IsSegmentOf(item.ID(), msg.ID) {
			break
		}
		n++
	}

	want := chat.NewAssistantMessageItems(sty, msg)
	if sameSegments(m.list, idx, n, want) {
		return false
	}
	items := make([]list.Item, len(want))
	for i, item := range want {
		items[i] = item
	}
	for i := range n {
		if item, ok := m.list.ItemAt(idx + i).(chat.MessageItem); ok {
			delete(m.idInxMap, item.ID())
		}
	}
	m.list.SpliceItems(idx, n, items...)
	m.reindexFrom(idx)
	return true
}

// sameSegments reports whether the n items at idx show what want would.
func sameSegments(l *list.List, idx, n int, want []chat.MessageItem) bool {
	if n != len(want) {
		return false
	}
	for i, w := range want {
		have, ok := l.ItemAt(idx + i).(chat.SegmentKeyed)
		wantKeyed, ok2 := w.(chat.SegmentKeyed)
		if !ok || !ok2 || have.SegmentKey() != wantKeyed.SegmentKey() {
			return false
		}
	}
	return true
}

// reindexFrom rebuilds the ID index for every item from idx on, including
// nested tool IDs, as SetMessages does.
func (m *Chat) reindexFrom(idx int) {
	for i := idx; i < m.list.Len(); i++ {
		item, ok := m.list.ItemAt(i).(chat.MessageItem)
		if !ok {
			continue
		}
		m.idInxMap[item.ID()] = i
		if container, ok := item.(chat.NestedToolContainer); ok {
			for _, nested := range container.NestedTools() {
				m.idInxMap[nested.ID()] = i
			}
		}
	}
}

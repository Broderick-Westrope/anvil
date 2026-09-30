package model

import (
	"context"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
)

type branchViewport struct {
	selectedID    string
	selectedIndex int
	offset        int
	follow        bool
}

func (m *Chat) branchViewport() branchViewport {
	s := branchViewport{selectedIndex: m.list.Selected(), offset: m.list.Offset(), follow: m.follow}
	if item, ok := m.SelectedItem().(chat.MessageItem); ok {
		s.selectedID = item.ID()
	}
	return s
}

func (m *Chat) restoreBranchViewport(s branchViewport) {
	index := s.selectedIndex
	if i, ok := m.idInxMap[s.selectedID]; ok {
		index = i
	}
	m.SetSelected(index)
	m.list.ScrollToTop()
	m.list.ScrollBy(s.offset)
	m.follow = s.follow
}

type branchNestedSnapshot struct {
	session  session.Session
	messages []message.Message
	nested   map[string]branchNestedSnapshot
}

func readBranchNested(ctx context.Context, ws workspace.Workspace, msgs []message.Message) (map[string]branchNestedSnapshot, error) {
	nested := make(map[string]branchNestedSnapshot)
	for _, msg := range msgs {
		for _, tc := range msg.ToolCalls() {
			if tc.Name != agent.TaskToolName && tc.Name != tools.AgenticFetchToolName {
				continue
			}
			id := ws.CreateAgentToolSessionID(msg.ID, tc.ID)
			children, err := ws.ListMessages(ctx, id)
			if err != nil {
				return nil, err
			}
			if len(children) == 0 {
				continue
			}
			sess, err := ws.GetSession(ctx, id)
			if err != nil {
				return nil, err
			}
			descendants, err := readBranchNested(ctx, ws, children)
			if err != nil {
				return nil, err
			}
			nested[tc.ID] = branchNestedSnapshot{session: sess, messages: children, nested: descendants}
		}
	}
	return nested, nil
}

func (m *UI) branchItems(msgs []message.Message, nested map[string]branchNestedSnapshot) []chat.MessageItem {
	ptrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		ptrs[i] = &msgs[i]
	}
	results := chat.BuildToolResultMap(ptrs)
	var items []chat.MessageItem
	var userTime int64
	for _, msg := range msgs {
		if msg.Role == message.User {
			userTime = msg.CreatedAt
		}
		messageItems := chat.ExtractMessageItems(m.com.Styles, &msg, results, m.expandedToolPatterns())
		if msg.Role == message.Assistant && msg.FinishReason() == message.FinishReasonEndTurn {
			messageItems = append(messageItems, chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(userTime, 0)))
		}
		for _, item := range messageItems {
			tool, ok := item.(chat.ToolMessageItem)
			if !ok {
				continue
			}
			child, ok := nested[tool.ToolCall().ID]
			if !ok {
				continue
			}
			container, ok := item.(chat.NestedToolContainer)
			if !ok {
				continue
			}
			childItems := m.branchItems(child.messages, child.nested)
			var childTools []chat.ToolMessageItem
			for _, item := range childItems {
				if tool, ok := item.(chat.ToolMessageItem); ok {
					if compact, ok := tool.(chat.Compactable); ok {
						compact.SetCompact(true)
					}
					childTools = append(childTools, tool)
				}
			}
			container.SetNestedTools(childTools)
			if da, ok := item.(chat.DrillableAgent); ok {
				da.SetChildSessionID(child.session.ID)
				da.SetHasChildMessages(true)
				for _, msg := range child.messages {
					if msg.Role == message.Assistant {
						da.IncrementTurns()
						da.SetModel(msg.Model)
					}
					da.IncrementToolCalls(len(msg.ToolCalls()))
				}
				da.SetTokens(child.session.PromptTokens + child.session.CompletionTokens)
				da.SetCost(child.session.Cost)
				da.SetStartedAt(time.Unix(child.session.CreatedAt, 0))
				if tool.HasResult() {
					da.SetFinishedAt(time.Unix(child.session.UpdatedAt, 0))
				}
			}
		}
		items = append(items, messageItems...)
	}
	m.lastUserMessageTime = userTime
	return items
}

func (m *UI) installBranchSnapshot(msgs []message.Message, nested map[string]branchNestedSnapshot) {
	m.chat.SetMessages(m.branchItems(msgs, nested)...)
	m.chat.SelectLast()
	m.chat.ScrollToBottom()
}

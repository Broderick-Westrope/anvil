package model

import (
	"context"
	"reflect"
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

type branchItemSnapshot struct {
	message  message.Message
	results  []message.ToolResult
	nested   map[string]branchNestedSnapshot
	userTime int64
	items    []chat.MessageItem
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

func (m *UI) branchItems(msgs []message.Message, nested map[string]branchNestedSnapshot, previous map[string]branchItemSnapshot) ([]chat.MessageItem, map[string]branchItemSnapshot) {
	ptrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		ptrs[i] = &msgs[i]
	}
	results := chat.BuildToolResultMap(ptrs)
	next := make(map[string]branchItemSnapshot, len(msgs))
	var items []chat.MessageItem
	var userTime int64
	for _, msg := range msgs {
		if msg.Role == message.User {
			userTime = msg.CreatedAt
		}
		entry := branchItemSnapshot{message: msg, nested: make(map[string]branchNestedSnapshot)}
		if msg.Role == message.Assistant {
			entry.userTime = userTime
		}
		for _, tc := range msg.ToolCalls() {
			if result, ok := results[tc.ID]; ok {
				entry.results = append(entry.results, result)
			}
			if child, ok := nested[tc.ID]; ok {
				entry.nested[tc.ID] = child
			}
		}
		old, ok := previous[msg.ID]
		oldItems := old.items
		old.items = nil
		if ok && reflect.DeepEqual(old, entry) {
			entry.items = oldItems
		} else {
			entry.items = chat.ExtractMessageItems(m.com.Styles, &msg, results, m.expandedToolPatterns())
			if msg.Role == message.Assistant && msg.FinishReason() == message.FinishReasonEndTurn {
				entry.items = append(entry.items, chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(userTime, 0)))
			}
			for i, item := range entry.items {
				for _, previous := range oldItems {
					if previous.ID() != item.ID() {
						continue
					}
					if assistant, ok := previous.(*chat.AssistantMessageItem); ok {
						assistant.SetMessage(&msg)
						entry.items[i] = previous
					}
					oldTool, ok := previous.(chat.ToolMessageItem)
					if !ok {
						break
					}
					newTool, ok := item.(chat.ToolMessageItem)
					if !ok {
						break
					}
					if _, nestedContainer := previous.(chat.NestedToolContainer); nestedContainer {
						if reflect.DeepEqual(old.nested[newTool.ToolCall().ID], entry.nested[newTool.ToolCall().ID]) && reflect.DeepEqual(oldTool.ToolCall(), newTool.ToolCall()) && reflect.DeepEqual(old.results, entry.results) {
							entry.items[i] = previous
						}
						break
					}
					if !reflect.DeepEqual(oldTool.ToolCall(), newTool.ToolCall()) {
						oldTool.SetToolCall(newTool.ToolCall())
					}
					var previousResult *message.ToolResult
					for i := range old.results {
						if old.results[i].ToolCallID == newTool.ToolCall().ID {
							previousResult = &old.results[i]
							break
						}
					}
					var nextResult *message.ToolResult
					if result, ok := results[newTool.ToolCall().ID]; ok {
						nextResult = &result
					}
					if !reflect.DeepEqual(previousResult, nextResult) {
						oldTool.SetResult(nextResult)
					}
					entry.items[i] = previous
					break
				}
				if entry.items[i] != item {
					continue
				}
				tool, ok := item.(chat.ToolMessageItem)
				if !ok {
					continue
				}
				child, ok := entry.nested[tool.ToolCall().ID]
				if !ok {
					continue
				}
				container, ok := item.(chat.NestedToolContainer)
				if !ok {
					continue
				}
				childItems, _ := m.branchItems(child.messages, child.nested, nil)
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
		}
		next[msg.ID] = entry
		items = append(items, entry.items...)
	}
	m.lastUserMessageTime = userTime
	return items, next
}

func (m *UI) installBranchSnapshot(msgs []message.Message, nested map[string]branchNestedSnapshot, run *branchRun) {
	var previous map[string]branchItemSnapshot
	if run != nil {
		previous = run.items
	}
	items, next := m.branchItems(msgs, nested, previous)
	if run != nil {
		run.items = next
	}
	same := len(items) == m.chat.Len()
	if same {
		for i, item := range items {
			if item != m.chat.ItemAt(i) {
				same = false
				break
			}
		}
	}
	if same {
		if m.chat.Follow() {
			m.chat.ScrollToBottom()
		}
		return
	}
	viewport := m.chat.branchViewport()
	m.chat.SetMessages(items...)
	m.chat.restoreBranchViewport(viewport)
	if previous == nil || viewport.follow {
		m.chat.SelectLast()
		m.chat.ScrollToBottom()
	}
}

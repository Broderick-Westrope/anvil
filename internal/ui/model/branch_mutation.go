package model

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

type mutationRefreshRequest struct {
	id        string
	epoch     uint64
	sessionID string
}

type mutationRefreshResultMsg struct {
	request  *mutationRefreshRequest
	session  session.Session
	messages []message.Message
	nested   map[string]branchNestedSnapshot
	err      error
}

func (m *UI) mutationRefreshCmd(request *mutationRefreshRequest) tea.Cmd {
	m.mutationReadEpoch++
	copy := *request
	copy.epoch = m.mutationReadEpoch
	request = &copy
	ws := m.com.Workspace
	return func() tea.Msg {
		result := mutationRefreshResultMsg{request: request}
		ctx := context.Background()
		result.session, result.err = ws.GetSession(ctx, request.sessionID)
		if result.err != nil {
			return result
		}
		if result.session.LeafMessageID != "" {
			result.messages, result.err = ws.GetBranchPath(ctx, result.session.LeafMessageID)
			if result.err != nil {
				return result
			}
		}
		result.nested, result.err = readBranchNested(ctx, ws, result.messages)
		return result
	}
}

func (m *UI) handleMutationRefresh(msg mutationRefreshResultMsg) tea.Cmd {
	if _, ok := m.pendingMutations[msg.request.id]; !ok {
		return nil
	}
	if m.session == nil || m.session.ID != msg.request.sessionID {
		m.endMutation(msg.request.id)
		return nil
	}
	if msg.err != nil {
		m.mutationReload = msg.request
		return util.ReportError(fmt.Errorf("conversation refresh failed; use Retry branch reload: %w", msg.err))
	}
	if msg.request.epoch == m.mutationReadEpoch {
		m.session = &msg.session
		viewport := m.chat.branchViewport()
		m.installBranchSnapshot(msg.messages, msg.nested, m.branchRun)
		m.chat.restoreBranchViewport(viewport)
	}
	m.endMutation(msg.request.id)
	if m.mutationReload != nil && m.mutationReload.id == msg.request.id {
		m.mutationReload = nil
	}
	return nil
}

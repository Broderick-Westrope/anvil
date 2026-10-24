package model

import (
	"context"
	"database/sql"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

// The test workspaces embed workspace.Workspace, so they answer the
// subagent steering calls the drill-in view makes on every render: no
// task call is ever running in them.

func (w *workflowTestWorkspace) AgentSubagentRunning(string) bool           { return false }
func (w *workflowTestWorkspace) AgentSubagentPendingSteers(string) []string { return nil }
func (w *workflowTestWorkspace) GetSubagentCall(context.Context, string) (session.SubagentCall, error) {
	return session.SubagentCall{}, sql.ErrNoRows
}

func (w *jobsWorkspace) AgentSubagentRunning(string) bool           { return false }
func (w *jobsWorkspace) AgentSubagentPendingSteers(string) []string { return nil }
func (w *jobsWorkspace) GetSubagentCall(context.Context, string) (session.SubagentCall, error) {
	return session.SubagentCall{}, sql.ErrNoRows
}

func (w *workflowTestWorkspace) AgentTakeReturnedSteers(string) []agent.SubagentSteersReturned {
	return nil
}

func (w *jobsWorkspace) AgentTakeReturnedSteers(string) []agent.SubagentSteersReturned { return nil }

func (w *animWorkspace) AgentSubagentRunning(string) bool { return false }

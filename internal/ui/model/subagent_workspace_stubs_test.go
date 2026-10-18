package model

import "context"

// The test workspaces embed workspace.Workspace, so they answer the
// subagent steering calls the drill-in view makes on every render: no
// task call is ever running in them.

func (w *workflowTestWorkspace) AgentSubagentRunning(string) bool           { return false }
func (w *workflowTestWorkspace) AgentSubagentPendingSteers(string) []string { return nil }
func (w *workflowTestWorkspace) SubagentCallSession(_ context.Context, callID string) string {
	return callID
}

func (w *jobsWorkspace) AgentSubagentRunning(string) bool           { return false }
func (w *jobsWorkspace) AgentSubagentPendingSteers(string) []string { return nil }
func (w *jobsWorkspace) SubagentCallSession(_ context.Context, callID string) string {
	return callID
}

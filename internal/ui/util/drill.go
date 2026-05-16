package util

// DrillInMsg requests the UI to drill into a subagent session.
type DrillInMsg struct {
	SessionID string
	Label     string
}

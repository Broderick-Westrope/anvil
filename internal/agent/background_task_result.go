package agent

// BackgroundTaskResult holds the outcome of a completed background task.
type BackgroundTaskResult struct {
	TaskID    string
	AgentName string
	Result    string
	Success   bool
	Cost      float64
}

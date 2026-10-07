package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// maxJobEventsPerNotice bounds how many events one notice carries; the
// rest stay pending for the next step.
const maxJobEventsPerNotice = 5

// maxConsecutiveWakes bounds how many wake runs a session gets without
// user input, so a chatty job can't keep an unattended agent busy.
const maxConsecutiveWakes = 3

// deliverJobEvents claims up to five pending events for the session,
// persists them as one job_event message after parentID, and marks
// them delivered. On failure the events are released for a later
// step. It returns nil when nothing was pending.
func (a *sessionAgent) deliverJobEvents(ctx context.Context, sessionID, parentID string) (*message.Message, error) {
	if a.jobEvents == nil {
		return nil, nil
	}
	events, remaining := a.jobEvents.Claim(sessionID, maxJobEventsPerNotice)
	if len(events) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}

	msg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:            message.User,
		MessageType:     message.MessageTypeJobEvent,
		Parts:           []message.ContentPart{message.TextContent{Text: jobevents.FormatNotice(events, remaining, time.Now())}},
		ParentMessageID: parentID,
	})
	if err != nil {
		a.jobEvents.Release(ids)
		return nil, fmt.Errorf("failed to create job event message: %w", err)
	}
	a.jobEvents.MarkDelivered(ids)
	return &msg, nil
}

// observeJobResult records job facts that a persisted, successful tool
// result showed the agent, so their notifications are not repeated.
func (a *sessionAgent) observeJobResult(toolName, metadata string) {
	if metadata == "" {
		return
	}
	switch toolName {
	case tools.JobOutputToolName:
		var meta tools.JobOutputResponseMetadata
		if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
			slog.Debug("Failed to decode job_output metadata", "error", err)
			return
		}
		if meta.ShellID == "" {
			return
		}
		if meta.Done {
			a.jobEvents.Observe(meta.ShellID, jobevents.KindCompleted, 0)
		}
		if meta.EndReason == string(shell.WaitMatched) && meta.WatchGen > 0 {
			a.jobEvents.Observe(meta.ShellID, jobevents.KindMatched, meta.WatchGen)
		}
	case tools.JobKillToolName:
		var meta tools.JobKillResponseMetadata
		if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
			slog.Debug("Failed to decode job_kill metadata", "error", err)
			return
		}
		if meta.Exited && meta.ShellID != "" {
			a.jobEvents.Observe(meta.ShellID, jobevents.KindCompleted, 0)
		}
	}
}

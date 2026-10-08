package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/message"
)

type runState struct {
	summaryFailed  bool
	acceptedUserID string
	assistantIDs   []string
	firstMessage   bool
}

func (a *sessionAgent) restoreAttempt(ctx context.Context, sessionID string, state *runState) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, id := range state.assistantIDs {
		m, err := a.messages.Get(persistCtx, id)
		if err != nil {
			return fmt.Errorf("reading retry placeholder: %w", err)
		}
		if m.Role != message.Assistant || m.MessageType != message.MessageTypeMessage {
			return errors.New("retry would replay meaningful output")
		}
		for _, part := range m.Parts {
			switch p := part.(type) {
			case message.Finish:
			case message.TextContent:
				if p.Text != "" {
					return errors.New("retry would replay meaningful output")
				}
			default:
				return errors.New("retry would replay meaningful output")
			}
		}
	}
	if err := a.sessions.MoveLeaf(persistCtx, sessionID, state.acceptedUserID); err != nil {
		return fmt.Errorf("restoring retry parent: %w", err)
	}
	for _, id := range state.assistantIDs {
		if err := a.messages.Delete(persistCtx, id); err != nil {
			return fmt.Errorf("deleting retry placeholder: %w", err)
		}
	}
	state.assistantIDs = nil
	return nil
}

package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

var (
	ErrBranchStaleSource           = errors.New("branch source has changed")
	ErrBranchInvalidTarget         = errors.New("invalid branch target")
	ErrBranchUnsafePrefix          = errors.New("unsafe branch prefix")
	ErrBranchUnsupportedAttachment = errors.New("unsupported branch attachment or model capability")
)

type BranchOrigin struct {
	TargetMessageID      string
	ExpectedSourceLeafID string
}

type BranchRunOptions struct {
	Origin               BranchOrigin
	OnUserMessageCreated func(message.Message)
}

type runState struct {
	summaryFailed  bool
	origin         *BranchOrigin
	callback       func(message.Message)
	acceptedUserID string
	attemptParent  string
	assistantIDs   []string
	branch         bool
	continuation   bool
	firstMessage   bool
}

func (c *coordinator) RunFromMessage(ctx context.Context, sessionID, prompt string, opts BranchRunOptions, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	return c.submitRun(ctx, sessionID, prompt, &opts, attachments)
}

func cloneAttachments(attachments []message.Attachment) []message.Attachment {
	result := slices.Clone(attachments)
	for i := range result {
		result[i].Content = slices.Clone(result[i].Content)
	}
	return result
}

func validateBranchParts(parts []message.ContentPart, supportsImages bool) error {
	for _, part := range parts {
		switch p := part.(type) {
		case message.TextContent, message.Finish:
		case message.BinaryContent:
			att := message.Attachment{MimeType: p.MIMEType}
			if !att.IsText() && (!att.IsImage() || !supportsImages) {
				return ErrBranchUnsupportedAttachment
			}
		default:
			return ErrBranchUnsupportedAttachment
		}
	}
	return nil
}

func validateBranchPrefix(path []message.Message) error {
	seen := make(map[string]bool)
	pending := make(map[string]string)
	for _, m := range path {
		if m.MessageType != "" && m.MessageType != message.MessageTypeMessage {
			continue
		}
		if len(pending) > 0 && m.Role != message.Tool {
			return fmt.Errorf("interrupted tool exchange: %w", ErrBranchUnsafePrefix)
		}
		for _, call := range m.ToolCalls() {
			if m.Role != message.Assistant || call.ID == "" || seen[call.ID] || !call.Finished {
				return fmt.Errorf("invalid tool call: %w", ErrBranchUnsafePrefix)
			}
			seen[call.ID] = true
			pending[call.ID] = call.Name
		}
		for _, result := range m.ToolResults() {
			name, ok := pending[result.ToolCallID]
			if m.Role != message.Tool || !ok || name != result.Name {
				return fmt.Errorf("invalid tool result: %w", ErrBranchUnsafePrefix)
			}
			delete(pending, result.ToolCallID)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("open tool exchange: %w", ErrBranchUnsafePrefix)
	}
	return nil
}

func (a *sessionAgent) branchSession(ctx context.Context, sess session.Session, origin BranchOrigin) (session.Session, error) {
	if sess.ParentSessionID != "" || origin.TargetMessageID == "" {
		return sess, ErrBranchInvalidTarget
	}
	if sess.LeafMessageID != origin.ExpectedSourceLeafID {
		return sess, ErrBranchStaleSource
	}
	target, err := a.messages.Get(ctx, origin.TargetMessageID)
	if err != nil {
		return sess, fmt.Errorf("loading branch target: %w: %w", ErrBranchInvalidTarget, err)
	}
	if target.SessionID != sess.ID || (target.MessageType != "" && target.MessageType != message.MessageTypeMessage) {
		return sess, ErrBranchInvalidTarget
	}
	path, err := a.messages.GetBranchPath(ctx, sess.LeafMessageID)
	if err != nil {
		return sess, err
	}
	index := slices.IndexFunc(path, func(m message.Message) bool { return m.ID == target.ID })
	if index < 0 {
		return sess, ErrBranchInvalidTarget
	}
	switch target.Role {
	case message.User:
		if err := validateBranchParts(target.Parts, a.largeModel.Get().CatwalkCfg.SupportsImages); err != nil {
			return sess, err
		}
		sess.LeafMessageID = target.ParentMessageID
		path = path[:index]
	case message.Assistant:
		if target.FinishReason() != message.FinishReasonEndTurn || len(target.ToolCalls()) != 0 {
			return sess, ErrBranchInvalidTarget
		}
		sess.LeafMessageID = target.ID
		path = path[:index+1]
	default:
		return sess, ErrBranchInvalidTarget
	}
	if err := validateBranchPrefix(path); err != nil {
		return sess, err
	}
	filtered := message.FilterBranchPathForContext(path)
	if err := validateBranchPrefix(filtered); err != nil {
		return sess, err
	}
	for _, m := range filtered {
		if m.Role == message.User {
			if err := validateBranchParts(m.Parts, a.largeModel.Get().CatwalkCfg.SupportsImages); err != nil {
				return sess, err
			}
		}
	}
	return sess, nil
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
			return fmt.Errorf("retry would replay meaningful output: %w", ErrBranchUnsafePrefix)
		}
		for _, part := range m.Parts {
			switch p := part.(type) {
			case message.Finish:
			case message.TextContent:
				if p.Text != "" {
					return fmt.Errorf("retry would replay meaningful output: %w", ErrBranchUnsafePrefix)
				}
			default:
				return fmt.Errorf("retry would replay meaningful output: %w", ErrBranchUnsafePrefix)
			}
		}
	}
	if err := a.sessions.MoveLeaf(persistCtx, sessionID, state.attemptParent); err != nil {
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

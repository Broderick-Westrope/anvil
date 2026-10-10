package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

// summaryRun holds the state of one summarize run: the compaction
// placeholder the summary streams into, and the contexts it writes with.
// Like turn's, its methods are the stream callbacks and the failure
// cleanup.
type summaryRun struct {
	a         *sessionAgent
	sessionID string
	// genCtx is the request context. persistCtx survives its cancellation,
	// so the cleanup after a canceled summary still lands.
	genCtx     context.Context
	persistCtx context.Context
	msg        *message.Message
}

func (s *summaryRun) onReasoningDelta(_ string, text string) error {
	s.msg.AppendReasoningContent(text)
	return s.a.messages.Update(s.genCtx, *s.msg)
}

func (s *summaryRun) onReasoningEnd(_ string, reasoning fantasy.ReasoningContent) error {
	// Handle anthropic signature.
	if anthropicData, ok := reasoning.ProviderMetadata["anthropic"]; ok {
		if signature, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok && signature.Signature != "" {
			s.msg.AppendReasoningSignature(signature.Signature)
		}
	}
	s.msg.FinishThinking()
	return s.a.messages.Update(s.genCtx, *s.msg)
}

func (s *summaryRun) onTextDelta(_, text string) error {
	s.msg.AppendContent(text)
	return s.a.messages.Update(s.genCtx, *s.msg)
}

// recordFailure ends a summary whose stream failed with err. A canceled or
// unauthorized request drops the placeholder and moves the leaf back to
// its parent, as if no summary was asked for. Any other error is shown on
// the placeholder. It returns err, or the error that stopped the cleanup.
func (s *summaryRun) recordFailure(err error) error {
	var providerErr *fantasy.ProviderError
	if errors.Is(err, context.Canceled) || s.genCtx.Err() != nil ||
		(errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized) {
		if moveErr := s.a.sessions.MoveLeaf(s.persistCtx, s.sessionID, s.msg.ParentMessageID); moveErr != nil {
			return fmt.Errorf("restoring summary parent: %w", moveErr)
		}
		if deleteErr := s.a.messages.Delete(s.persistCtx, s.msg.ID); deleteErr != nil {
			return fmt.Errorf("deleting summary placeholder: %w", deleteErr)
		}
		return err
	}
	s.msg.AddFinish(message.FinishReasonError, "Summarization Error", err.Error())
	if updateErr := s.a.messages.Update(s.persistCtx, *s.msg); updateErr != nil {
		return updateErr
	}
	return err
}

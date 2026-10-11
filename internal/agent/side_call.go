package agent

import (
	"context"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/google/uuid"
)

// sideCall is one model request made beside the conversation's turns: a
// summary, a title or a small-model completion. Like a turn, its methods
// are the fantasy callbacks. They frame the request for the provider the
// way a turn does and record its step usage.
type sideCall struct {
	a           *sessionAgent
	prefix      string
	providerCfg config.ProviderConfig
	runID       string
	kind        string
	agentName   string
	sessionID   string
	parentID    string
	messageID   string
	// model and attempt name the model making the request. A call that
	// falls back to another model sets them before each attempt.
	model   Model
	attempt int
	capture *stepCapture
}

// newSideCall starts a side call of kind for sessionID, which is empty for
// calls outside any session. It snapshots the provider prefix and config
// so a concurrent SetModels can't change them mid-call.
func (a *sessionAgent) newSideCall(kind, sessionID string, model Model) *sideCall {
	return &sideCall{
		a:           a,
		prefix:      a.systemPromptPrefix.Get(),
		providerCfg: a.providerConfig.Get(),
		runID:       uuid.NewString(),
		kind:        kind,
		agentName:   a.agentName,
		sessionID:   sessionID,
		model:       model,
	}
}

// streamCall returns the stream call for prompt with the side call's
// callbacks set.
func (s *sideCall) streamCall(prompt string) fantasy.AgentStreamCall {
	onRetry, onStreamFinish := s.a.captureCallbacks(&s.capture)
	return fantasy.AgentStreamCall{
		Prompt:         prompt,
		PrepareStep:    s.prepareStep,
		OnRetry:        onRetry,
		OnStreamFinish: onStreamFinish,
	}
}

func (s *sideCall) prepareStep(ctx context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
	prepared.Messages = frameMessages(s.prefix, s.providerCfg, options.Messages)
	if s.a.usageRecorder != nil {
		s.capture = s.a.newCapture(s.kind, s.runID, s.sessionID, s.parentID, s.model, nil, prepared.Messages)
		s.capture.agent = s.agentName
		s.capture.messageID = s.messageID
		s.capture.attempt = s.attempt
		s.capture.stepIndex = options.StepNumber
	}
	return ctx, prepared, nil
}

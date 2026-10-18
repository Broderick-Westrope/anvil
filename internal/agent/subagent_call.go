package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// maxSubagentResumes is how many times one task call resumes its subagent
// after a transient failure. Fantasy has already retried each step by the
// time a failure ends a run, so a couple of widely spaced resumes is enough.
const maxSubagentResumes = 2

// defaultSubagentRetryDelays are the waits before each automatic resume.
var defaultSubagentRetryDelays = []time.Duration{5 * time.Second, 30 * time.Second}

const (
	// subagentContinueText is the harness's message to a subagent resumed
	// after a transient failure.
	subagentContinueText = "Your previous attempt stopped because of a transient error. Continue where you left off."
	// subagentInterruptedText continues a subagent whose interrupting
	// steer was already delivered before its attempt was cancelled.
	subagentInterruptedText = "You were interrupted. Continue, taking the latest message into account."
	// subagentCostTimeout bounds the cost rollup after each attempt, which
	// runs even when the call was cancelled.
	subagentCostTimeout = 5 * time.Second
)

// SubagentSteersReturned reports human steers that a task call ended
// without delivering, so the UI can give the text back.
type SubagentSteersReturned struct {
	// SessionID is the subagent's child session.
	SessionID string
	Steers    []string
	// Reason says why the steers weren't delivered.
	Reason string
}

// subAgentParams holds the parameters for running a sub-agent.
type subAgentParams struct {
	Agent SessionAgent
	// AgentType and Depth are stored as the new subagent's identity. A
	// task subagent has an AgentType; other callers, such as
	// agentic_fetch, leave it empty and get no identity, handle or
	// automatic resume.
	AgentType      string
	Depth          int
	SessionID      string
	AgentMessageID string
	ToolCallID     string
	Prompt         string
	SessionTitle   string
	// Resume, when set, continues this existing subagent with Prompt
	// instead of starting a new one.
	Resume *session.Subagent
	// OnSession, when set, is called with the child session's ID before
	// the agent runs, for custom session configuration.
	OnSession func(sessionID string)
}

// childSession describes the session a subagent runs in.
type childSession struct {
	ID, ParentID, Title string
	// Existing marks a child session that already exists, such as a
	// resumed subagent's.
	Existing bool
	// OnCreate, when set, is called once the session exists.
	OnCreate func(sessionID string)
}

// inChildSession runs fn in a child session of s.ParentID, creating it
// unless it exists, then rolls the child's new cost up to the parent and
// hands its surviving background jobs to the parent. It returns the
// hand-off inventory, empty when there were no jobs. Every subagent run,
// from the task tool or a workflow, goes through here.
func (c *coordinator) inChildSession(ctx context.Context, s childSession, fn func(ctx context.Context, sessionID string) error) (handedOff string, err error) {
	// Detach from any submission owner ctx carries: the child's turns are
	// admitted as their own submissions, not run in place as, or rejected
	// as, the parent's owner.
	ctx = context.WithValue(ctx, ownerKey{}, struct{}{})
	sessionID := s.ID
	if !s.Existing {
		created, err := c.sessions.CreateTaskSession(ctx, s.ID, s.ParentID, s.Title)
		if err != nil {
			return "", fmt.Errorf("create session: %w", err)
		}
		sessionID = created.ID
	}
	defer func() {
		handedOff = handOffSubagentJobs(shell.GetBackgroundShellManager(), c.jobEvents, sessionID, s.ParentID)
	}()
	defer c.permissions.RevokeAutoApproveSession(sessionID)
	defer c.rollUpChildCost(ctx, sessionID)
	if s.OnCreate != nil {
		s.OnCreate(sessionID)
	}
	return "", fn(ctx, sessionID)
}

// rollUpChildCost adds the child's cost since the last rollup to its
// parent. It's best effort, and runs even after cancellation: a failure
// here must not discard output the subagent already produced.
func (c *coordinator) rollUpChildCost(ctx context.Context, childID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subagentCostTimeout)
	defer cancel()
	if err := c.sessions.RollUpChildCost(ctx, childID); err != nil {
		slog.Warn("Failed to roll up subagent cost", "child_session", childID, "error", err)
	}
}

// errSubAgentRun marks a failure the task tool reports back to the model
// as a tool error rather than failing the tool call.
type errSubAgentRun struct{ msg string }

func (e errSubAgentRun) Error() string { return e.msg }

// taskCall is one task tool call: a new subagent or a resume, holding the
// lease on its child session from start to final result.
type taskCall struct {
	// id is the call's agent tool session ID (messageID$$toolCallID).
	id     string
	lease  *subagentLease
	params subAgentParams
	handle string
	output string
	// recoveries names each transient failure the call resumed after.
	recoveries []string
}

// runSubAgent runs a task call in its child session and returns the
// subagent's output as the tool's response. Transient failures resume the
// same child automatically; every response ends with the subagent's
// handle so the caller can resume it.
func (c *coordinator) runSubAgent(ctx context.Context, params subAgentParams) (fantasy.ToolResponse, error) {
	callID := c.sessions.CreateAgentToolSessionID(params.AgentMessageID, params.ToolCallID)
	child := childSession{ID: callID, ParentID: params.SessionID, Title: params.SessionTitle, OnCreate: params.OnSession}
	kind := session.SubagentCallNew
	tc := &taskCall{id: callID, params: params}
	if params.Resume != nil {
		child.ID, child.Existing, kind = params.Resume.SessionID, true, session.SubagentCallResume
		tc.handle = params.Resume.Handle
	}

	callCtx, cancelCall := context.WithCancelCause(ctx)
	defer cancelCall(nil)
	lease, err := c.leases.acquire(child.ID, callID, cancelCall)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Subagent %s is running. Resume it after its current task call returns.", tc.handle)), nil
	}
	defer c.leases.release(lease)
	tc.lease = lease

	handedOff, err := c.inChildSession(callCtx, child, func(ctx context.Context, sessionID string) error {
		if err := c.recordTaskCall(ctx, tc, sessionID, kind); err != nil {
			return err
		}
		return c.runTaskAttempts(ctx, tc, sessionID)
	})
	if undelivered := lease.finalise(); len(undelivered) > 0 {
		c.handBackSteers(child.ID, undelivered, err)
	}

	var runErr errSubAgentRun
	var resp fantasy.ToolResponse
	// An if chain, not a tagless switch: coverage never marks a case
	// expression as run, so mutation testing would skip its conditions.
	if errors.As(err, &runErr) {
		resp = fantasy.NewTextErrorResponse(runErr.msg)
	} else if err != nil {
		if handedOff != "" {
			slog.Warn("Subagent jobs handed off after error", "parent_session", params.SessionID, "inventory", handedOff)
		}
		return fantasy.ToolResponse{}, err
	} else if tc.output == "" {
		resp = fantasy.NewTextErrorResponse(errNoTextOutput.Error())
	} else {
		resp = fantasy.NewTextResponse(tc.output)
		if recovered := tc.recoveryLine(); recovered != "" {
			resp.Content += "\n\n" + recovered
		}
	}
	if handedOff != "" {
		resp.Content += "\n\n" + handedOff
	}
	if summary := c.injectionSummary(ctx, child.ID, callID); summary != "" {
		resp.Content += "\n\n" + summary
	}
	if tc.handle != "" {
		resp.Content += "\n\nsubagent_id: " + tc.handle
	}
	return resp, nil
}

// recordTaskCall stores a new subagent's identity, and every call's link
// to its child session, before the child's first step.
func (c *coordinator) recordTaskCall(ctx context.Context, tc *taskCall, sessionID string, kind session.SubagentCallKind) error {
	if tc.params.AgentType == "" {
		return nil
	}
	if tc.params.Resume == nil {
		model := tc.params.Agent.Model().ModelCfg
		sub, err := c.sessions.CreateSubagent(ctx, session.Subagent{
			SessionID:       sessionID,
			ParentSessionID: tc.params.SessionID,
			Origin:          session.SubagentOriginTask,
			AgentType:       tc.params.AgentType,
			Depth:           tc.params.Depth,
			Provider:        model.Provider,
			Model:           model.Model,
		})
		if err != nil {
			return fmt.Errorf("store subagent identity: %w", err)
		}
		tc.handle = sub.Handle
	}
	if err := c.sessions.CreateSubagentCall(ctx, session.SubagentCall{ID: tc.id, SessionID: sessionID, Kind: kind}); err != nil {
		return fmt.Errorf("link task call to subagent: %w", err)
	}
	return nil
}

// runTaskAttempts runs the subagent until it finishes with no steers left
// to deliver, resuming it after interrupts and transient failures. Each
// attempt continues the same child session with a new message: the
// orchestrator's prompt, a human steer, or the harness's continue.
func (c *coordinator) runTaskAttempts(ctx context.Context, tc *taskCall, sessionID string) error {
	next := SessionAgentCall{Prompt: tc.params.Prompt}
	if tc.params.Resume != nil {
		next = tc.injection(message.InjectionContent{
			Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, Text: tc.params.Prompt,
		})
	}
	for {
		call, err := c.subAgentCall(ctx, tc.params.Agent, sessionID, next.Prompt)
		if err != nil {
			tc.lease.restore(humanSteers(next.Injections))
		}
		if errors.Is(err, errRefreshSubAgentModels) {
			return errSubAgentRun{"Failed to " + err.Error()}
		}
		if err != nil {
			return err
		}
		// Awaited, so the call's and attempt's contexts govern the run
		// even after it continues past auto-compaction.
		call.Await = true
		call.Injections, call.steering, call.state = next.Injections, tc.lease, &runState{}
		attemptCtx := tc.lease.beginAttempt(ctx)
		result, runErr := tc.params.Agent.Run(attemptCtx, call)
		c.rollUpChildCost(ctx, sessionID)
		if runErr != nil && call.state.acceptedUserID == "" {
			// The turn never persisted its message, so steers it carried
			// weren't delivered.
			tc.lease.restore(humanSteers(next.Injections))
		}

		if runErr == nil {
			if steers := tc.lease.finaliseOrTake(); len(steers) > 0 {
				next = SessionAgentCall{Injections: steers}
				continue
			}
			tc.output = subAgentOutput(result)
			return nil
		}
		if errors.Is(runErr, errLocalPersistence) {
			return tc.failure(ctx, runErr)
		}
		if ctx.Err() == nil && errors.Is(context.Cause(attemptCtx), errSubagentInterrupted) {
			next = tc.afterInterrupt()
			continue
		}
		retry, reason := classifySubagentFailure(ctx, runErr)
		resumes := len(tc.recoveries)
		if !retry || resumes >= maxSubagentResumes || tc.params.AgentType == "" {
			return tc.failure(ctx, runErr)
		}
		if err := waitBeforeResume(ctx, tc.lease, subagentRetryDelay(runErr, c.subagentRetryDelay(resumes))); err != nil {
			return tc.failure(ctx, err)
		}
		tc.recoveries = append(tc.recoveries, reason)
		next = tc.injection(message.InjectionContent{
			Source: message.InjectionSourceHarness, Kind: message.InjectionKindRetry, Text: subagentContinueText,
			Attempt: resumes + 1, MaxAttempts: maxSubagentResumes, Reason: reason,
		})
	}
}

func (c *coordinator) subagentRetryDelay(resumes int) time.Duration {
	delays := c.subagentRetryDelays
	if delays == nil {
		delays = defaultSubagentRetryDelays
	}
	return delays[min(resumes, len(delays)-1)]
}

// waitBeforeResume waits out a retry backoff. An interrupt ends the wait
// early; cancelling the call ends it with the call's error.
func waitBeforeResume(ctx context.Context, lease *subagentLease, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lease.wake:
		return nil
	case <-timer.C:
		return nil
	}
}

// humanSteers returns the steers among injections, which go back to the
// human when they aren't delivered.
func humanSteers(injections []message.InjectionContent) []message.InjectionContent {
	var steers []message.InjectionContent
	for _, inj := range injections {
		if inj.Source == message.InjectionSourceHuman {
			steers = append(steers, inj)
		}
	}
	return steers
}

// injection returns a run that starts with inj, recorded against the call.
func (tc *taskCall) injection(inj message.InjectionContent) SessionAgentCall {
	inj.CallID = tc.id
	return SessionAgentCall{Injections: []message.InjectionContent{inj}}
}

// afterInterrupt returns the run that continues the call after
// interrupt+steer: the human's pending steers, or a harness continue when
// the cancelled attempt had already delivered them.
func (tc *taskCall) afterInterrupt() SessionAgentCall {
	if steers := tc.lease.takePending(); len(steers) > 0 {
		return SessionAgentCall{Injections: steers}
	}
	return tc.injection(message.InjectionContent{
		Source: message.InjectionSourceHarness, Kind: message.InjectionKindContinue, Text: subagentInterruptedText,
	})
}

// failure returns the tool error for a call that ended with err.
func (tc *taskCall) failure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errCancelledByHuman) {
			return errSubAgentRun{"Subagent cancelled by human."}
		}
		return errSubAgentRun{"Subagent cancelled by parent or run cancel."}
	}
	msg := fmt.Sprintf("Failed to generate response: %s", err)
	if attempts := len(tc.recoveries) + 1; attempts > 1 {
		msg += fmt.Sprintf(" (after %d attempts)", attempts)
	}
	return errSubAgentRun{msg}
}

// recoveryLine summarises the transient failures a successful call
// recovered from, or returns "" when there were none.
func (tc *taskCall) recoveryLine() string {
	n := len(tc.recoveries)
	if n == 0 {
		return ""
	}
	noun := "failure"
	if n > 1 {
		noun = "failures"
	}
	return fmt.Sprintf("recovered after %d transient %s: %s", n, noun, strings.Join(tc.recoveries, ", "))
}

// injectionSummary lists what was injected into the child during the call,
// from the persisted injection records, so compaction can't drop entries.
// The orchestrator's own resume prompt is left out.
func (c *coordinator) injectionSummary(ctx context.Context, childID, callID string) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subagentCostTimeout)
	defer cancel()
	msgs, err := c.messages.List(ctx, childID)
	if err != nil {
		slog.Warn("Failed to list subagent messages for injection summary", "child_session", childID, "error", err)
		return ""
	}
	var lines []string
	for _, msg := range msgs {
		if msg.MessageType != message.MessageTypeSubagentInjection {
			continue
		}
		for _, inj := range msg.Injections() {
			if inj.CallID == callID && inj.Source != message.InjectionSourceOrchestrator {
				lines = append(lines, "- "+injectionSummaryLine(inj))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Injected during this run:\n" + strings.Join(lines, "\n")
}

func injectionSummaryLine(inj message.InjectionContent) string {
	switch inj.Kind {
	case message.InjectionKindInterruptSteer:
		return "human (interrupt+steer): " + inj.Text
	case message.InjectionKindRetry:
		return fmt.Sprintf("harness: resumed after %s (retry %d/%d)", inj.Reason, inj.Attempt, inj.MaxAttempts)
	case message.InjectionKindContinue:
		return "harness: continued after an interrupt"
	default:
		return "human (steer): " + inj.Text
	}
}

// handBackSteers tells the UI which accepted steers the call ended without
// delivering, so the human gets their text back.
func (c *coordinator) handBackSteers(childID string, undelivered []message.InjectionContent, callErr error) {
	texts := make([]string, 0, len(undelivered))
	for _, inj := range undelivered {
		texts = append(texts, inj.Text)
	}
	reason := "The subagent finished before it could read your message."
	if callErr != nil {
		reason = "The subagent's task call ended before it could read your message: " + callErr.Error()
	}
	c.subagentBroker().PublishMustDeliver(context.Background(), pubsub.CreatedEvent, SubagentSteersReturned{SessionID: childID, Steers: texts, Reason: reason})
}

func (c *coordinator) subagentBroker() *pubsub.Broker[SubagentSteersReturned] {
	c.subagentEventsOnce.Do(func() {
		if c.subagentEvents == nil {
			c.subagentEvents = pubsub.NewBroker[SubagentSteersReturned]()
		}
	})
	return c.subagentEvents
}

// SubscribeSubagentEvents implements Coordinator.
func (c *coordinator) SubscribeSubagentEvents(ctx context.Context) <-chan pubsub.Event[SubagentSteersReturned] {
	return c.subagentBroker().Subscribe(ctx)
}

// SteerSubagent implements Coordinator.
func (c *coordinator) SteerSubagent(childID, text string, interrupt bool) error {
	lease := c.leases.get(childID)
	if lease == nil {
		return fmt.Errorf("subagent is not running: %w", errSteerRejected)
	}
	return lease.steer(text, interrupt)
}

// CancelSubagent implements Coordinator.
func (c *coordinator) CancelSubagent(childID string) bool {
	lease := c.leases.get(childID)
	if lease == nil {
		return false
	}
	lease.cancel(errCancelledByHuman)
	return true
}

// SubagentRunning implements Coordinator.
func (c *coordinator) SubagentRunning(childID string) bool {
	return c.leases.get(childID) != nil
}

// SubagentPendingSteers implements Coordinator.
func (c *coordinator) SubagentPendingSteers(childID string) []string {
	lease := c.leases.get(childID)
	if lease == nil {
		return nil
	}
	return lease.pendingTexts()
}

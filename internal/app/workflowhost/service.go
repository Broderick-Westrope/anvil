package workflowhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// ErrRunInProgress refuses a second workflow in a session that already
// has one running.
var ErrRunInProgress = errors.New("a workflow is already running in this session")

// ErrShuttingDown refuses new workflows once Anvil is shutting down.
var ErrShuttingDown = errors.New("anvil is shutting down")

// ErrNoRun is returned by controls when the session has no workflow run.
var ErrNoRun = errors.New("no workflow is running in this session")

// EventKind says what an Event is about.
type EventKind string

const (
	// EventApproval asks the user to approve a script before it runs.
	EventApproval EventKind = "approval"
	// EventAsk is a question from ask().
	EventAsk EventKind = "ask"
	// EventStatus is a progress update from status().
	EventStatus EventKind = "status"
	// EventFocus asks the UI to move to a session.
	EventFocus EventKind = "focus"
	// EventWarn is a non-fatal problem, such as undeclared use.
	EventWarn EventKind = "warn"
	// EventStep is a bash command or turn the script started, shown as a
	// workflow step.
	EventStep EventKind = "step"
	// EventState reports a change in a run's State.
	EventState EventKind = "state"
	// EventAgent reports a subagent starting (Status running) and ending
	// (completed or failed, with Err).
	EventAgent EventKind = "agent"
	// EventFinished ends an invocation.
	EventFinished EventKind = "finished"
)

// RunState is the change an EventState reports.
type RunState string

const (
	// StateStarted is a run that passed approval and began executing.
	StateStarted RunState = "started"
	// StatePaused is a run that issues no new primitives until resumed.
	StatePaused RunState = "paused"
	// StateResumed is a paused run continuing.
	StateResumed RunState = "resumed"
	// StateHeld is a run holding its sessions inside workflow.exclusive.
	StateHeld RunState = "held"
	// StateReleased is a run whose sessions are no longer held.
	StateReleased RunState = "released"
)

// Event is everything the UI needs to show a workflow. Fields beyond the
// identifying ones belong to the Kinds named beside them.
type Event struct {
	Kind      EventKind
	RunID     string
	Command   string
	SessionID string // The invoking session.

	// RequestID identifies an approval or ask awaiting Respond.
	RequestID string
	Approval  *ApprovalRequest
	Question  string
	Options   []string

	Progress *Progress  // EventStatus.
	Activity *Activity  // EventStep.
	Agent    *AgentCall // EventAgent.
	Focus    string     // EventFocus: the session to move to.
	State    RunState   // EventState.

	// Message is an EventWarn's warning, or why an EventFinished run
	// didn't complete.
	Message string
	// Status and Result are how an EventFinished run ended.
	Status Status
	Result json.RawMessage
}

// Progress is what a script last reported with status().
type Progress struct {
	Step, Detail string
}

// ActivityKind says what an EventStep started.
type ActivityKind string

const (
	// ActivityBash is a bash command.
	ActivityBash ActivityKind = "bash"
	// ActivityTurn is a turn sent to a session.
	ActivityTurn ActivityKind = "turn"
)

// Activity is a bash command or turn a script started.
type Activity struct {
	Kind ActivityKind
	// Cmd is an ActivityBash's command line.
	Cmd string
	// Session is the session an ActivityTurn runs in.
	Session string
}

// AgentCall is a subagent a script ran.
type AgentCall struct {
	Name  string
	Model string // The pinned model, if any.
	// Session is the subagent's child session, empty when it failed
	// before it had one.
	Session string
	// Status is running, completed or failed, with Err saying why.
	Status Status
	Err    string
}

// ApprovalRequest is what the user reviews before a workflow first runs
// or after any file in its directory changes.
type ApprovalRequest struct {
	ScriptPath string
	Origin     string
	// Files is every file in the workflow's directory, the script first,
	// plus files removed since the last approval.
	Files    []ApprovalFile
	Requires workflow.Requirements
}

// Invocation is a workflow command the user ran.
type Invocation struct {
	CommandID   string
	CommandName string
	// Source is the command's origin: "" (user), "project" or
	// "plugin:<name>".
	Source     string
	ScriptPath string
	SessionID  string
	Raw        string
}

// Deps are the services a Service drives.
type Deps struct {
	Coordinator agent.Coordinator
	Sessions    session.Service
	Store       *Store
	WorkingDir  string
	// SkipApproval reports whether approval is skipped (--yolo).
	SkipApproval func() bool
	// RepublishInterval is how often unanswered requests are published
	// again; zero means the default.
	RepublishInterval time.Duration
}

// Service starts workflow runs and connects them to the UI.
type Service struct {
	deps     Deps
	lifetime context.Context
	stop     context.CancelFunc
	events   *pubsub.Broker[Event]
	wg       sync.WaitGroup

	mu       sync.Mutex
	stopping bool
	// stopReason says why Shutdown stopped the service, for the runs it
	// interrupts.
	stopReason string
	runs       map[string]*activeRun // Keyed by invoking session.
	pending    map[string]pendingRequest
}

// pendingRequest is an approval or ask awaiting Respond. A non-empty
// options restricts the answers Respond accepts.
type pendingRequest struct {
	reply   chan string
	options []string
}

type activeRun struct {
	id      string
	command string
	run     *workflow.Run
}

// NewService creates a Service. Runs live as long as lifetime.
func NewService(lifetime context.Context, deps Deps) *Service {
	lifetime, stop := context.WithCancel(lifetime)
	return &Service{
		deps:     deps,
		lifetime: lifetime,
		stop:     stop,
		events:   pubsub.NewBroker[Event](),
		runs:     map[string]*activeRun{},
		pending:  map[string]pendingRequest{},
	}
}

// Subscribe streams workflow events.
func (s *Service) Subscribe(ctx context.Context) <-chan pubsub.Event[Event] {
	return s.events.Subscribe(ctx)
}

// Catalog reports which skills and agents exist, for checking a script's
// requirements.
func (s *Service) Catalog() workflow.Catalog { return catalog{s.deps.Coordinator} }

// Start validates the invocation and runs it in the background: approval
// first if needed, then the script. It returns an error only for problems
// found before anything runs; later outcomes arrive as EventFinished.
func (s *Service) Start(ctx context.Context, inv Invocation) error {
	if inv.SessionID == "" {
		return errors.New("workflow needs a session")
	}
	script, err := workflow.Load(inv.ScriptPath)
	if err != nil {
		return err
	}
	if len(script.Problems) > 0 {
		return fmt.Errorf("workflow %s can't run: %s", inv.CommandName, strings.Join(script.Problems, "; "))
	}
	if missing := script.Missing(s.Catalog()); len(missing) > 0 {
		return fmt.Errorf("workflow %s can't run: %s", inv.CommandName, strings.Join(missing, "; "))
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return ErrShuttingDown
	}
	// A session belongs to at most one run, so controls and holds always
	// know which run they mean.
	if _, active := s.findLocked(inv.SessionID); active != nil || s.runs[inv.SessionID] != nil {
		s.mu.Unlock()
		return ErrRunInProgress
	}
	// Reserve the session until the run is registered or refused.
	s.runs[inv.SessionID] = &activeRun{}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		s.invoke(inv, script)
	}()
	return nil
}

// Shutdown refuses new workflows, ends pending approvals and questions,
// aborts running scripts and waits, up to ctx, for them to record their
// outcome, then closes every subscription. Runs it ends are recorded as
// interrupted, not aborted, with reason as the cause.
func (s *Service) Shutdown(ctx context.Context, reason string) error {
	s.mu.Lock()
	s.stopping, s.stopReason = true, reason
	var runs []*workflow.Run
	for _, active := range s.runs {
		if active.run != nil {
			runs = append(runs, active.run)
		}
	}
	s.mu.Unlock()
	s.stop()
	for _, run := range runs {
		run.Abort()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	defer s.events.Shutdown()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for workflows to stop: %w", ctx.Err())
	}
}

// stopped reports whether Shutdown has begun, and why.
func (s *Service) stopped() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping, s.stopReason
}

// runOutcome classifies how a run ended. Shutdown aborting a run makes it
// interrupted, but a run that failed on its own stays failed even if
// shutdown had begun.
func runOutcome(err error, stopping bool, reason string) (Status, string) {
	cancelled := errors.Is(err, workflow.ErrAborted) || errors.Is(err, context.Canceled)
	switch {
	case err == nil:
		return StatusCompleted, ""
	case cancelled && stopping:
		return StatusInterrupted, "interrupted: " + reason
	case errors.Is(err, workflow.ErrAborted):
		return StatusAborted, "aborted"
	default:
		return StatusFailed, err.Error()
	}
}

func (s *Service) invoke(inv Invocation, script *workflow.Script) {
	ctx := s.lifetime
	base := Event{Command: inv.CommandName, SessionID: inv.SessionID}
	// finish ends every path. It frees the session before reporting, so
	// anyone reacting to "finished" can start the next run straight away.
	finish := func(runID string, status Status, result json.RawMessage, msg string) {
		s.mu.Lock()
		delete(s.runs, inv.SessionID)
		s.mu.Unlock()
		ev := base
		ev.Kind, ev.RunID, ev.Status, ev.Result, ev.Message = EventFinished, runID, status, result, msg
		s.events.PublishMustDeliver(context.WithoutCancel(ctx), pubsub.UpdatedEvent, ev)
	}

	// The bundle is read once, before approval, and the run reads its
	// references from it, so what runs is what was approved.
	current, err := loadBundle(script)
	if err != nil {
		finish("", StatusFailed, nil, err.Error())
		return
	}
	approved, err := s.approve(ctx, inv, script, current)
	if err != nil {
		stopping, reason := s.stopped()
		status, msg := runOutcome(err, stopping, reason)
		finish("", status, nil, msg)
		return
	}
	if !approved {
		finish("", StatusDenied, nil, "workflow not approved")
		return
	}

	runID := uuid.NewString()
	rec := RunRecord{
		ID:                runID,
		Command:           inv.CommandID,
		CommandSource:     inv.Source,
		ScriptPath:        script.Path,
		ScriptHash:        script.Hash,
		BundleHash:        current.hash(),
		InvokingSessionID: inv.SessionID,
		Input:             inv.Raw,
		WorkingDir:        canonicalDir(s.deps.WorkingDir),
	}
	if err := s.deps.Store.StartRun(ctx, rec); err != nil {
		finish("", StatusFailed, nil, err.Error())
		return
	}
	base.RunID = runID
	b := &backend{svc: s, base: base}
	run := workflow.NewRun(script, b, s.deps.Store.Recorder(runID), workflow.Options{
		InvokingSessionID: inv.SessionID,
		Tmpdir:            s.deps.Store.Tmpdir(runID),
		References:        current.references(),
	})
	s.mu.Lock()
	stopping, reason := s.stopping, s.stopReason
	if !stopping {
		s.runs[inv.SessionID] = &activeRun{id: runID, command: inv.CommandName, run: run}
	}
	s.mu.Unlock()
	if stopping {
		// Shutdown began after this run was recorded but before it could
		// be registered for Shutdown to abort; it never starts.
		msg := "interrupted: " + reason
		if ferr := s.deps.Store.FinishRun(context.WithoutCancel(ctx), runID, StatusInterrupted, nil, msg); ferr != nil {
			slog.Warn("Failed to record workflow run outcome", "run", runID, "error", ferr)
		}
		finish(runID, StatusInterrupted, nil, msg)
		return
	}
	b.publishState(ctx, Event{Kind: EventState, State: StateStarted})

	result, err := run.Execute(ctx, workflow.Input{Raw: inv.Raw})
	stopping, reason = s.stopped()
	status, msg := runOutcome(err, stopping, reason)
	if ferr := s.deps.Store.FinishRun(context.WithoutCancel(ctx), runID, status, result, msg); ferr != nil {
		slog.Warn("Failed to record workflow run outcome", "run", runID, "error", ferr)
	}
	finish(runID, status, result, msg)
}

// approve asks the user to approve the current bundle unless this exact
// bundle was approved before or approval is skipped.
func (s *Service) approve(ctx context.Context, inv Invocation, script *workflow.Script, current bundle) (bool, error) {
	if s.deps.SkipApproval != nil && s.deps.SkipApproval() {
		return true, nil
	}
	previous, ok, err := s.deps.Store.approval(ctx, script.Path)
	if err != nil {
		return false, err
	}
	if ok && previous.hash() == current.hash() {
		return true, nil
	}
	origin := inv.Source
	if origin == "" {
		origin = "personal"
	}
	answer, err := s.request(ctx, Event{
		Kind: EventApproval, Command: inv.CommandName, SessionID: inv.SessionID,
		Approval: &ApprovalRequest{
			ScriptPath: script.Path, Origin: origin, Requires: script.Requires,
			Files: approvalFiles(filepath.Base(script.Path), current, previous),
		},
		Options: []string{"approve", "deny"},
	})
	if err != nil {
		return false, err
	}
	if answer != "approve" {
		return false, nil
	}
	return true, s.deps.Store.approve(ctx, script.Path, current, origin)
}

// request publishes ev with a fresh request ID and waits for Respond.
func (s *Service) request(ctx context.Context, ev Event) (string, error) {
	ev.RequestID = uuid.NewString()
	reply := make(chan string, 1)
	s.mu.Lock()
	s.pending[ev.RequestID] = pendingRequest{reply: reply, options: ev.Options}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, ev.RequestID)
		s.mu.Unlock()
	}()
	// Delivery can drop an event (a slow subscriber, a full buffer), and a
	// question nobody sees would block the run forever, so it's published
	// again until answered. The UI ignores repeats of a request it shows.
	every := s.deps.RepublishInterval
	if every <= 0 {
		every = defaultRepublishInterval
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		s.events.PublishMustDeliver(ctx, pubsub.UpdatedEvent, ev)
		select {
		case answer := <-reply:
			return answer, nil
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// defaultRepublishInterval is how often an unanswered approval or question
// is published again.
const defaultRepublishInterval = 2 * time.Second

// Respond answers an approval ("approve" or "deny") or an ask(). An ask
// with options accepts only one of them.
func (s *Service) Respond(requestID, answer string) error {
	s.mu.Lock()
	req, ok := s.pending[requestID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending workflow request %s", requestID)
	}
	if len(req.options) > 0 && !slices.Contains(req.options, answer) {
		return fmt.Errorf("answer %q isn't one of %s", answer, strings.Join(req.options, ", "))
	}
	select {
	case req.reply <- answer:
	default:
	}
	return nil
}

// Pause, Resume and Abort control the run that involves sessionID, which
// may be its invoking session or any session it created.
func (s *Service) Pause(sessionID string) error {
	return s.control(sessionID, StatePaused, func(r *workflow.Run) { r.Pause() })
}

// Resume continues a paused run.
func (s *Service) Resume(sessionID string) error {
	return s.control(sessionID, StateResumed, func(r *workflow.Run) { r.Resume() })
}

// Abort cancels a run's in-flight work and ends it as aborted. Its
// EventFinished reports the outcome, so it publishes no state.
func (s *Service) Abort(sessionID string) error {
	return s.control(sessionID, "", func(r *workflow.Run) { r.Abort() })
}

func (s *Service) control(sessionID string, state RunState, fn func(*workflow.Run)) error {
	invoker, active := s.find(sessionID)
	if active == nil {
		return ErrNoRun
	}
	fn(active.run)
	if state != "" {
		s.events.PublishMustDeliver(context.WithoutCancel(s.lifetime), pubsub.UpdatedEvent, Event{
			Kind: EventState, RunID: active.id, Command: active.command, SessionID: invoker, State: state,
		})
	}
	return nil
}

// Running reports the invoking session of the run involving sessionID, if
// any.
func (s *Service) Running(sessionID string) (invoker string, ok bool) {
	invoker, active := s.find(sessionID)
	return invoker, active != nil
}

func (s *Service) find(sessionID string) (string, *activeRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findLocked(sessionID)
}

// findLocked is find for callers holding s.mu.
func (s *Service) findLocked(sessionID string) (string, *activeRun) {
	for invoker, active := range s.runs {
		if active.run == nil {
			continue
		}
		if invoker == sessionID {
			return invoker, active
		}
		for _, id := range active.run.Sessions() {
			if id == sessionID {
				return invoker, active
			}
		}
	}
	return "", nil
}

func canonicalDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

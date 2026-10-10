package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// StatusDenied ends an invocation whose script wasn't approved. It never
// has a run row.
const StatusDenied Status = "denied"

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
	// EventState reports a run starting, pausing, resuming or holding
	// sessions.
	EventState EventKind = "state"
	// EventAgent reports a subagent starting (Status running) and ending
	// (completed or failed, with Message). Step is the agent, Detail its
	// pinned model and Target its child session.
	EventAgent EventKind = "agent"
	// EventFinished ends an invocation.
	EventFinished EventKind = "finished"
)

// Event is everything the UI needs to show a workflow. Fields beyond the
// identifying ones depend on Kind.
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

	Step    string
	Detail  string
	Message string
	// Target is the session for EventFocus, the session a turn runs in
	// for an EventStep with Step "turn", and the child session for
	// EventAgent.
	Target string

	Paused bool
	Held   bool

	Status Status
	Result json.RawMessage
}

// ApprovalRequest is what the user reviews before a script first runs or
// after it changes.
type ApprovalRequest struct {
	ScriptPath string
	Origin     string
	Source     string
	// PreviousSource is the last approved version, empty on first run.
	PreviousSource string
	Requires       workflow.Requirements
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
	runs     map[string]*activeRun // Keyed by invoking session.
	pending  map[string]chan string
}

type activeRun struct {
	id  string
	run *workflow.Run
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
		pending:  map[string]chan string{},
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
	if missing := script.Missing(s.Catalog()); len(missing) > 0 {
		return fmt.Errorf("workflow %s can't run: %s", inv.CommandName, strings.Join(missing, "; "))
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return ErrShuttingDown
	}
	if s.runs[inv.SessionID] != nil {
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
// outcome. Runs it ends are recorded as interrupted, not aborted.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
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
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for workflows to stop: %w", ctx.Err())
	}
}

func (s *Service) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

func (s *Service) invoke(inv Invocation, script *workflow.Script) {
	ctx := s.lifetime
	defer func() {
		s.mu.Lock()
		delete(s.runs, inv.SessionID)
		s.mu.Unlock()
	}()
	base := Event{Command: inv.CommandName, SessionID: inv.SessionID}
	finish := func(runID string, status Status, result json.RawMessage, msg string) {
		ev := base
		ev.Kind, ev.RunID, ev.Status, ev.Result, ev.Message = EventFinished, runID, status, result, msg
		s.events.PublishMustDeliver(context.WithoutCancel(ctx), pubsub.UpdatedEvent, ev)
	}

	approved, err := s.approve(ctx, inv, script)
	if err != nil {
		finish("", StatusFailed, nil, err.Error())
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
		InvokingSessionID: inv.SessionID,
		Input:             inv.Raw,
		WorkingDir:        canonicalDir(s.deps.WorkingDir),
	}
	if err := s.deps.Store.StartRun(ctx, rec); err != nil {
		finish("", StatusFailed, nil, err.Error())
		return
	}
	base.RunID = runID
	b := &backend{catalog: catalog{s.deps.Coordinator}, svc: s, base: base}
	run := workflow.NewRun(script, b, s.deps.Store.Recorder(runID), workflow.Options{
		InvokingSessionID: inv.SessionID,
		Tmpdir:            s.deps.Store.Tmpdir(runID),
	})
	s.mu.Lock()
	s.runs[inv.SessionID] = &activeRun{id: runID, run: run}
	s.mu.Unlock()
	started := base
	started.Kind = EventState
	s.events.Publish(pubsub.UpdatedEvent, started)

	result, err := run.Execute(ctx, workflow.Input{Raw: inv.Raw})
	status, msg := StatusCompleted, ""
	switch {
	case err != nil && s.isStopping():
		status, msg = StatusInterrupted, "interrupted: Anvil shut down"
	case errors.Is(err, workflow.ErrAborted):
		status, msg = StatusAborted, "aborted"
	case err != nil:
		status, msg = StatusFailed, err.Error()
	}
	if ferr := s.deps.Store.FinishRun(context.WithoutCancel(ctx), runID, status, result, msg); ferr != nil {
		slog.Warn("Failed to record workflow run outcome", "run", runID, "error", ferr)
	}
	finish(runID, status, result, msg)
}

// approve asks the user to approve the script unless this exact version
// was approved before or approval is skipped.
func (s *Service) approve(ctx context.Context, inv Invocation, script *workflow.Script) (bool, error) {
	if s.deps.SkipApproval != nil && s.deps.SkipApproval() {
		return true, nil
	}
	hash, previous, ok, err := s.deps.Store.Approval(ctx, script.Path)
	if err != nil {
		return false, err
	}
	if ok && hash == script.Hash {
		return true, nil
	}
	origin := inv.Source
	if origin == "" {
		origin = "personal"
	}
	answer, err := s.request(ctx, Event{
		Kind: EventApproval, Command: inv.CommandName, SessionID: inv.SessionID,
		Approval: &ApprovalRequest{
			ScriptPath: script.Path, Origin: origin, Source: script.Source,
			PreviousSource: previous, Requires: script.Requires,
		},
		Options: []string{"approve", "deny"},
	})
	if err != nil {
		return false, err
	}
	if answer != "approve" {
		return false, nil
	}
	return true, s.deps.Store.Approve(ctx, script.Path, script.Hash, script.Source, origin)
}

// request publishes ev with a fresh request ID and waits for Respond.
func (s *Service) request(ctx context.Context, ev Event) (string, error) {
	ev.RequestID = uuid.NewString()
	reply := make(chan string, 1)
	s.mu.Lock()
	s.pending[ev.RequestID] = reply
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, ev.RequestID)
		s.mu.Unlock()
	}()
	s.events.PublishMustDeliver(ctx, pubsub.UpdatedEvent, ev)
	select {
	case answer := <-reply:
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Respond answers an approval ("approve" or "deny") or an ask().
func (s *Service) Respond(requestID, answer string) error {
	s.mu.Lock()
	reply, ok := s.pending[requestID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending workflow request %s", requestID)
	}
	select {
	case reply <- answer:
	default:
	}
	return nil
}

// Pause, Resume and Abort control the run that involves sessionID, which
// may be its invoking session or any session it created.
func (s *Service) Pause(sessionID string) error {
	return s.control(sessionID, func(r *workflow.Run) { r.Pause() })
}

// Resume continues a paused run.
func (s *Service) Resume(sessionID string) error {
	return s.control(sessionID, func(r *workflow.Run) { r.Resume() })
}

// Abort cancels a run's in-flight work and ends it as aborted.
func (s *Service) Abort(sessionID string) error {
	return s.control(sessionID, func(r *workflow.Run) { r.Abort() })
}

func (s *Service) control(sessionID string, fn func(*workflow.Run)) error {
	invoker, active := s.find(sessionID)
	if active == nil {
		return ErrNoRun
	}
	fn(active.run)
	s.events.Publish(pubsub.UpdatedEvent, Event{
		Kind: EventState, RunID: active.id, SessionID: invoker, Paused: active.run.Paused(),
	})
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

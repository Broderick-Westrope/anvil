// Package branchsummaries owns branch summary runs: at most one per root
// session, started and cancelled here and reported to the UI as events.
package branchsummaries

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
)

// ErrAlreadyRunning means the session already has a summary running.
var ErrAlreadyRunning = errors.New("a branch summary is already running for this session")

// State is where a run is.
type State string

const (
	StateRunning   State = "running"
	StateStored    State = "stored"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	// StateStale means nothing was stored because the conversation moved
	// on, or there was nothing to summarize.
	StateStale State = "stale"
)

// Event reports a run starting or ending.
type Event struct {
	SessionID      string
	RunID          string
	Agent          string
	ChildSessionID string
	State          State
	// SummaryID is the stored summary, for StateStored.
	SummaryID string
	// Err is why the run failed, for StateFailed.
	Err error
}

// Coordinator is what the service asks of the agent coordinator.
type Coordinator interface {
	ReserveSession(sessionID string) (release func())
	SummarizeBranch(ctx context.Context, req agent.BranchSummaryRequest) (message.Message, error)
}

type run struct {
	id     string
	cancel context.CancelFunc
}

// Service starts, cancels and reports branch summary runs.
type Service struct {
	events      *pubsub.Broker[Event]
	ctx         context.Context
	coordinator func() Coordinator

	mu   sync.Mutex
	runs map[string]*run
	wg   sync.WaitGroup
}

// New returns a service whose runs live until ctx ends. coordinator
// returns the current coordinator, which the app may replace.
func New(ctx context.Context, coordinator func() Coordinator) *Service {
	return &Service{
		events:      pubsub.NewBroker[Event](),
		ctx:         ctx,
		coordinator: coordinator,
		runs:        map[string]*run{},
	}
}

// Subscribe returns the service's events.
func (s *Service) Subscribe(ctx context.Context) <-chan pubsub.Event[Event] {
	return s.events.Subscribe(ctx)
}

// Start begins summarizing req's branch and returns the run's ID. The
// session is reserved before Start returns, so a message sent afterwards
// queues behind the summary.
func (s *Service) Start(req agent.BranchSummaryRequest) (string, error) {
	c := s.coordinator()
	if c == nil {
		return "", errors.New("agent coordinator not initialized")
	}
	if req.ChildSessionID == "" {
		req.ChildSessionID = uuid.NewString()
	}

	s.mu.Lock()
	if _, ok := s.runs[req.SessionID]; ok {
		s.mu.Unlock()
		return "", ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancel(s.ctx)
	r := &run{id: uuid.NewString(), cancel: cancel}
	s.runs[req.SessionID] = r
	release := c.ReserveSession(req.SessionID)
	s.mu.Unlock()

	base := Event{SessionID: req.SessionID, RunID: r.id, Agent: req.Agent, ChildSessionID: req.ChildSessionID}
	running := base
	running.State = StateRunning
	s.events.PublishMustDeliver(s.ctx, pubsub.UpdatedEvent, running)

	s.wg.Go(func() {
		defer cancel()
		summary, err := c.SummarizeBranch(ctx, req)
		s.mu.Lock()
		delete(s.runs, req.SessionID)
		s.mu.Unlock()
		// Sends queued behind the summary start only after its outcome is
		// published.
		defer release()

		end := base
		switch {
		case err == nil:
			end.State, end.SummaryID = StateStored, summary.ID
		case errors.Is(err, context.Canceled):
			end.State = StateCancelled
		case errors.Is(err, agent.ErrStaleBranchSummary), errors.Is(err, agent.ErrNothingToSummarize):
			end.State = StateStale
		default:
			slog.Warn("Branch summary failed", "session", req.SessionID, "agent", req.Agent, "error", err)
			end.State, end.Err = StateFailed, err
		}
		s.events.PublishMustDeliver(s.ctx, pubsub.UpdatedEvent, end)
	})
	return r.id, nil
}

// Cancel stops the session's running summary and reports whether there
// was one.
func (s *Service) Cancel(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[sessionID]
	if ok {
		r.cancel()
	}
	return ok
}

// Running reports whether the session has a summary running.
func (s *Service) Running(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runs[sessionID]
	return ok
}

// Shutdown cancels every run and waits for them to end.
func (s *Service) Shutdown() {
	s.mu.Lock()
	for _, r := range s.runs {
		r.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.events.Shutdown()
}

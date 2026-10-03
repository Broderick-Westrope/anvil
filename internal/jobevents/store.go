// Package jobevents tracks background job events that should be
// delivered to the agent session that owns the job.
package jobevents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/jobstore"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// retention is how long settled events and per-job bookkeeping for
// jobs no longer known to the owner are kept.
const retention = 10 * time.Minute

type Kind string

const (
	KindCompleted Kind = "completed"
	KindMatched   Kind = "matched"
)

type State string

const (
	StatePending    State = "pending"
	StateClaimed    State = "claimed" // Taken for delivery; message not yet persisted.
	StateDelivered  State = "delivered"
	StateSuperseded State = "superseded"
)

type Event struct {
	ID        string // <instance ID>-<per-process sequence>.
	JobID     string
	Kind      Kind
	WatchGen  uint64 // KindMatched only.
	Line      string // KindMatched only.
	Tail      string // KindCompleted only.
	Info      shell.JobInfo
	State     State
	CreatedAt time.Time

	settledAt time.Time // When the event became delivered or superseded.
	// claimable is false while a persisted event's row is not yet
	// committed, so no process can deliver it before it is durable.
	claimable bool
	// durable means the event has, or is getting, a database row.
	durable bool
}

// OwnerFunc returns the session that currently owns a job.
type OwnerFunc func(jobID string) (sessionID string, ok bool)

type Store struct {
	mu        sync.Mutex
	owner     OwnerFunc
	now       func() time.Time
	idPrefix  string
	nextID    int64
	persist   atomic.Pointer[persister] // Nil while memory-only.
	events    []*Event
	watchGens map[string]uint64      // Latest watch generation per job.
	observed  map[string]observation // What the agent has already seen per job.
	dropped   map[string]time.Time   // Jobs whose events are discarded (killed at handoff).
	touched   map[string]time.Time   // Last bookkeeping change per job, for pruning.
	signal    chan struct{}          // Capacity 1; see Pending.
}

type observation struct {
	completed  bool
	matchedGen uint64 // Highest watch generation whose match was observed.
}

var _ shell.EventSink = (*Store)(nil)

func NewStore(owner OwnerFunc) *Store {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return &Store{
		owner:     owner,
		now:       time.Now,
		idPrefix:  hex.EncodeToString(b[:]),
		watchGens: make(map[string]uint64),
		observed:  make(map[string]observation),
		dropped:   make(map[string]time.Time),
		touched:   make(map[string]time.Time),
		signal:    make(chan struct{}, 1),
	}
}

// Pending returns a channel that receives a value whenever a new event
// becomes pending. Sends are non-blocking and coalesced, so receivers
// must re-check HasPending for every session they care about.
func (s *Store) Pending() <-chan struct{} {
	return s.signal
}

// JobCompleted records a completion event. It only updates memory.
func (s *Store) JobCompleted(info shell.JobInfo, tail string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.dropped[info.ID]; ok {
		return
	}
	state := StatePending
	if s.observed[info.ID].completed {
		state = StateSuperseded
	}
	s.addLocked(&Event{JobID: info.ID, Kind: KindCompleted, Tail: tail, Info: info, State: state})
}

// WatchReplaced records a new watch generation and supersedes pending
// matches from older watches. It only updates memory.
func (s *Store) WatchReplaced(jobID string, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.touched[jobID] = now
	if gen > s.watchGens[jobID] {
		s.watchGens[jobID] = gen
	}
	for _, e := range s.events {
		if e.JobID == jobID && e.Kind == KindMatched && e.State == StatePending && e.WatchGen < s.watchGens[jobID] {
			s.settleLocked(e, StateSuperseded, now)
		}
	}
}

// PatternMatched records a watch match unless its watch has been
// replaced. It only updates memory.
func (s *Store) PatternMatched(info shell.JobInfo, gen uint64, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.dropped[info.ID]; ok {
		return
	}
	if gen < s.watchGens[info.ID] {
		return
	}
	state := StatePending
	if s.observed[info.ID].matchedGen >= gen {
		state = StateSuperseded
	}
	s.addLocked(&Event{JobID: info.ID, Kind: KindMatched, WatchGen: gen, Line: line, Info: info, State: state})
}

func (s *Store) addLocked(e *Event) {
	now := s.now()
	s.nextID++
	e.ID = fmt.Sprintf("%s-%d", s.idPrefix, s.nextID)
	e.CreatedAt = now
	e.claimable = true
	if e.State != StatePending {
		e.settledAt = now
	} else if p := s.persist.Load(); p != nil {
		if _, ok := jobstore.ParseID(e.JobID); ok && p.enqueue(op{kind: opInsert, event: *e}) {
			e.durable = true
			e.claimable = false
		}
	}
	s.events = append(s.events, e)
	s.touched[e.JobID] = now
	if e.State == StatePending && e.claimable {
		s.signalLocked()
	}
}

func (s *Store) signalLocked() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// settleLocked marks e delivered or superseded. Settled events need no
// row, since only pending and claimed events are loaded at startup.
func (s *Store) settleLocked(e *Event, state State, now time.Time) {
	e.State = state
	e.settledAt = now
	s.forgetRowLocked(e)
}

// forgetRowLocked deletes e's row, if it has one.
func (s *Store) forgetRowLocked(e *Event) {
	if !e.durable {
		return
	}
	e.durable = false
	if p := s.persist.Load(); p != nil {
		p.enqueue(op{kind: opDelete, id: e.ID})
	}
}

// markPersisted makes an event claimable once its row insert finished.
// If the insert failed, the event stays memory-only.
func (s *Store) markPersisted(id string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.ID != id {
			continue
		}
		e.claimable = true
		if !ok {
			e.durable = false
		}
		if e.State == StatePending {
			s.signalLocked()
		}
		return
	}
}

// Observe records that a persisted tool result showed the agent this
// fact. Matching pending events become superseded, and matching events
// created later are created superseded.
func (s *Store) Observe(jobID string, kind Kind, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.touched[jobID] = now
	obs := s.observed[jobID]
	switch kind {
	case KindCompleted:
		obs.completed = true
	case KindMatched:
		obs.matchedGen = max(obs.matchedGen, gen)
	}
	s.observed[jobID] = obs

	for _, e := range s.events {
		if e.JobID == jobID && e.State == StatePending && obs.covers(e) {
			s.settleLocked(e, StateSuperseded, now)
		}
	}
}

func (o observation) covers(e *Event) bool {
	switch e.Kind {
	case KindCompleted:
		return o.completed
	case KindMatched:
		return e.WatchGen <= o.matchedGen
	}
	return false
}

// Claim takes up to limit of the session's pending events, oldest first,
// for delivery. remaining is the number of the session's pending events
// left behind. With persistence, persisted events are also claimed in
// the database, and only those this process wins are returned, so two
// processes never deliver the same event.
func (s *Store) Claim(sessionID string, limit int) (claimed []Event, remaining int) {
	claimed, remaining = s.claimLocal(sessionID, limit)
	p := s.persist.Load()
	if p == nil || len(claimed) == 0 {
		return claimed, remaining
	}

	var durable []string
	for _, e := range claimed {
		if e.durable {
			durable = append(durable, e.ID)
		}
	}
	if len(durable) == 0 {
		return claimed, remaining
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	won, err := p.claim(ctx, durable, s.now())
	if err != nil {
		slog.Warn("Failed to claim background job events", "error", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	kept := claimed[:0]
	for _, c := range claimed {
		switch {
		case !c.durable || slices.Contains(won, c.ID):
			kept = append(kept, c)
		case err != nil:
			// The claim could not be confirmed; retry at a later step.
			if e := s.findLocked(c.ID); e != nil && e.State == StateClaimed {
				e.State = StatePending
			}
		default:
			// Another process claimed it and will deliver it.
			s.events = slices.DeleteFunc(s.events, func(e *Event) bool { return e.ID == c.ID })
		}
	}
	return kept, remaining
}

func (s *Store) findLocked(id string) *Event {
	for _, e := range s.events {
		if e.ID == id {
			return e
		}
	}
	return nil
}

func (s *Store) claimLocal(sessionID string, limit int) (claimed []Event, remaining int) {
	s.mu.Lock()
	now := s.now()
	s.pruneEventsLocked(now)
	var jobIDs []string
	for _, e := range s.events {
		if e.State == StatePending {
			jobIDs = append(jobIDs, e.JobID)
		}
	}
	stale := s.staleJobsLocked(now)
	s.mu.Unlock()

	// The owner func may take other locks, so call it without s.mu.
	owners := s.resolve(append(jobIDs, stale...))

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, jobID := range stale {
		if owners[jobID].known {
			continue
		}
		s.forgetJobLocked(jobID)
	}

	for _, e := range s.events {
		if e.State != StatePending || !e.claimable {
			continue
		}
		res, ok := owners[e.JobID]
		if !ok {
			// Created after the owners were resolved; the next claim
			// picks it up.
			continue
		}
		if res.sessionID(e) != sessionID {
			continue
		}
		if e.Kind == KindMatched && e.WatchGen < s.watchGens[e.JobID] {
			s.settleLocked(e, StateSuperseded, now)
			continue
		}
		if len(claimed) >= limit {
			remaining++
			continue
		}
		e.State = StateClaimed
		claimed = append(claimed, *e)
	}
	return claimed, remaining
}

// MarkDelivered marks claimed events as delivered.
func (s *Store) MarkDelivered(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for _, e := range s.events {
		if e.State == StateClaimed && slices.Contains(ids, e.ID) {
			s.settleLocked(e, StateDelivered, now)
		}
	}
}

// Release returns claimed events to pending, or supersedes them if
// they were observed or replaced while claimed. It does not signal.
func (s *Store) Release(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for _, e := range s.events {
		if e.State != StateClaimed || !slices.Contains(ids, e.ID) {
			continue
		}
		if s.observed[e.JobID].covers(e) || (e.Kind == KindMatched && e.WatchGen < s.watchGens[e.JobID]) {
			s.settleLocked(e, StateSuperseded, now)
			continue
		}
		e.State = StatePending
		if p := s.persist.Load(); p != nil && e.durable {
			p.enqueue(op{kind: opRelease, id: e.ID})
		}
	}
}

// HasPending reports whether the session owns any pending event.
func (s *Store) HasPending(sessionID string) bool {
	s.mu.Lock()
	var pending []*Event
	var jobIDs []string
	for _, e := range s.events {
		if e.State == StatePending && e.claimable {
			pending = append(pending, e)
			jobIDs = append(jobIDs, e.JobID)
		}
	}
	s.mu.Unlock()

	owners := s.resolve(jobIDs)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range pending {
		if e.State == StatePending && owners[e.JobID].sessionID(e) == sessionID {
			return true
		}
	}
	return false
}

// DropJobs discards the jobs' pending events and any created later.
func (s *Store) DropJobs(jobIDs []string) {
	if len(jobIDs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for _, id := range jobIDs {
		s.dropped[id] = now
		s.touched[id] = now
	}
	s.events = slices.DeleteFunc(s.events, func(e *Event) bool {
		drop := e.State == StatePending && slices.Contains(jobIDs, e.JobID)
		if drop {
			s.forgetRowLocked(e)
		}
		return drop
	})
}

// Reassign updates the Info.SessionID snapshots used after eviction.
func (s *Store) Reassign(jobIDs []string, toSession string) {
	if len(jobIDs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range s.events {
		if slices.Contains(jobIDs, e.JobID) {
			e.Info.SessionID = toSession
		}
	}
}

type ownerResult struct {
	session string
	known   bool
}

// sessionID returns the resolved owner, falling back to the event's
// snapshot when the owner no longer knows the job.
func (r ownerResult) sessionID(e *Event) string {
	if r.known {
		return r.session
	}
	return e.Info.SessionID
}

func (s *Store) resolve(jobIDs []string) map[string]ownerResult {
	owners := make(map[string]ownerResult, len(jobIDs))
	for _, id := range jobIDs {
		if _, ok := owners[id]; ok {
			continue
		}
		var res ownerResult
		if s.owner != nil {
			res.session, res.known = s.owner(id)
		}
		owners[id] = res
	}
	return owners
}

func (s *Store) pruneEventsLocked(now time.Time) {
	s.events = slices.DeleteFunc(s.events, func(e *Event) bool {
		settled := e.State == StateDelivered || e.State == StateSuperseded
		return settled && now.Sub(e.settledAt) > retention
	})
}

// staleJobsLocked returns jobs with bookkeeping but no events whose
// last change is older than the retention period.
func (s *Store) staleJobsLocked(now time.Time) []string {
	var stale []string
	for id, at := range s.touched {
		if now.Sub(at) <= retention {
			continue
		}
		if slices.ContainsFunc(s.events, func(e *Event) bool { return e.JobID == id }) {
			continue
		}
		stale = append(stale, id)
	}
	return stale
}

func (s *Store) forgetJobLocked(jobID string) {
	delete(s.watchGens, jobID)
	delete(s.observed, jobID)
	delete(s.dropped, jobID)
	delete(s.touched, jobID)
}

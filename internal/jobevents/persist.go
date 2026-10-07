package jobevents

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// persistTimeout bounds each database call made for event persistence.
const persistTimeout = 5 * time.Second

// ErrPersistenceClosed is returned for database work requested after
// [Store.Close].
var ErrPersistenceClosed = errors.New("job event persistence closed")

type opKind int

const (
	opInsert opKind = iota
	opDelete
	opRelease
)

type op struct {
	kind  opKind
	event Event  // opInsert.
	id    string // opDelete, opRelease.
}

// persister mirrors the store's in-memory mutations into the database.
// Mutations are queued in order and applied by one writer goroutine, so
// event producers never wait for the database.
type persister struct {
	q          db.Querier
	instanceID string
	onInserted func(id string, ok bool)

	ctx    context.Context // Canceled when Close gives up waiting.
	cancel context.CancelFunc

	mu       sync.Mutex
	queue    []op
	closed   bool
	enqueued uint64
	applied  uint64
	progress chan struct{} // Closed and replaced whenever applied grows.
	inflight sync.WaitGroup
	wake     chan struct{}
	stop     chan struct{}
	exited   chan struct{}
	stopOnce sync.Once
}

func newPersister(q db.Querier, instanceID string, onInserted func(string, bool)) *persister {
	ctx, cancel := context.WithCancel(context.Background())
	return &persister{
		q:          q,
		instanceID: instanceID,
		onInserted: onInserted,
		ctx:        ctx,
		cancel:     cancel,
		progress:   make(chan struct{}),
		wake:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
		exited:     make(chan struct{}),
	}
}

// enqueue queues a mutation. It never blocks and reports false once the
// persister is closed, in which case the mutation is dropped.
func (p *persister) enqueue(o op) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.queue = append(p.queue, o)
	p.enqueued++
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return true
}

func (p *persister) run() {
	defer close(p.exited)
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			closed := p.closed
			p.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-p.wake:
			case <-p.stop:
			}
			continue
		}
		o := p.queue[0]
		p.queue = p.queue[1:]
		p.mu.Unlock()

		p.apply(o)

		p.mu.Lock()
		p.applied++
		close(p.progress)
		p.progress = make(chan struct{})
		p.mu.Unlock()
	}
}

func (p *persister) apply(o op) {
	ctx, cancel := context.WithTimeout(p.ctx, persistTimeout)
	defer cancel()
	switch o.kind {
	case opInsert:
		e := o.event
		key, _ := jobstore.ParseID(e.JobID)
		err := p.q.CreateBackgroundJobEvent(ctx, db.CreateBackgroundJobEventParams{
			ID:        e.ID,
			JobID:     key,
			Kind:      string(e.Kind),
			WatchGen:  int64(e.WatchGen),
			Line:      e.Line,
			Tail:      e.Tail,
			CreatedAt: e.CreatedAt.UnixMilli(),
		})
		if err != nil {
			slog.Warn("Failed to save background job event; it will not survive a restart", "id", e.ID, "job_id", e.JobID, "error", err)
		}
		p.onInserted(e.ID, err == nil)
	case opDelete:
		if err := p.q.DeleteBackgroundJobEvent(ctx, o.id); err != nil {
			slog.Warn("Failed to delete settled background job event", "id", o.id, "error", err)
		}
	case opRelease:
		if err := p.q.ReleaseBackgroundJobEvent(ctx, o.id); err != nil {
			slog.Warn("Failed to release background job event claim", "id", o.id, "error", err)
		}
	}
}

// claim claims ids for this instance in the database and returns the
// ones it won. A pending event, or one this instance already claimed,
// can be won; one claimed by another instance cannot.
func (p *persister) claim(ctx context.Context, ids []string, now time.Time) ([]string, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrPersistenceClosed
	}
	p.inflight.Add(1)
	p.mu.Unlock()
	defer p.inflight.Done()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(p.ctx, cancel)()

	return p.q.ClaimBackgroundJobEvents(ctx, db.ClaimBackgroundJobEventsParams{
		ClaimedBy: p.instanceID,
		ClaimedAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true},
		Ids:       ids,
	})
}

// flush waits until every mutation queued before the call is applied.
func (p *persister) flush(ctx context.Context) error {
	p.mu.Lock()
	target := p.enqueued
	for p.applied < target {
		ch := p.progress
		p.mu.Unlock()
		select {
		case <-ch:
		case <-p.exited:
			return ErrPersistenceClosed
		case <-ctx.Done():
			return ctx.Err()
		}
		p.mu.Lock()
	}
	p.mu.Unlock()
	return nil
}

// close drops later mutations, drains queued ones, and waits for the
// writer and in-flight claims. If ctx expires first, outstanding
// database calls are canceled.
func (p *persister) close(ctx context.Context) {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.stop) })

	done := make(chan struct{})
	go func() {
		<-p.exited
		p.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("Timed out saving background job events; dropping the rest")
		p.cancel()
		<-done
	}
	p.cancel()
}

// Persist mirrors the store into the database: it loads events left
// pending by earlier runs (and claims held by Anvil instances that are
// no longer live), then saves later events. Event IDs are prefixed with
// instanceID. Call it before the store receives events.
func (s *Store) Persist(ctx context.Context, q db.Querier, instanceID string) error {
	s.mu.Lock()
	s.idPrefix = instanceID
	s.mu.Unlock()

	if err := s.load(ctx, q, instanceID); err != nil {
		return err
	}
	p := newPersister(q, instanceID, s.markPersisted)
	s.persist.Store(p)
	go p.run()
	return nil
}

func (s *Store) load(ctx context.Context, q db.Querier, instanceID string) error {
	instances, err := q.ListAnvilInstances(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	live := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if now.Sub(time.UnixMilli(inst.HeartbeatAt)) < jobstore.InstanceLiveWindow {
			live[inst.ID] = true
		}
	}

	rows, err := q.ListUndeliveredBackgroundJobEvents(ctx)
	if err != nil {
		return err
	}
	var loaded []*Event
	for _, row := range rows {
		if State(row.State) == StateClaimed {
			if live[row.ClaimedBy] && row.ClaimedBy != instanceID {
				continue
			}
			if err := q.ReleaseBackgroundJobEvent(ctx, row.ID); err != nil {
				return err
			}
		}
		info := shell.JobInfo{
			ID:          jobstore.FormatID(row.JobID),
			SessionID:   row.SessionID,
			Origin:      shell.JobOrigin(row.Origin),
			Command:     row.Command,
			Description: row.Description,
			WorkingDir:  row.WorkingDir,
			StartedAt:   time.UnixMilli(row.StartedAt),
		}
		if row.CompletedAt.Valid {
			info.Done = true
			info.CompletedAt = time.UnixMilli(row.CompletedAt.Int64)
			info.ExitCode = int(row.ExitCode.Int64)
			info.EndReason = row.EndReason.String
		}
		loaded = append(loaded, &Event{
			ID:        row.ID,
			JobID:     info.ID,
			Kind:      Kind(row.Kind),
			WatchGen:  uint64(row.WatchGen),
			Line:      row.Line,
			Tail:      row.Tail,
			Info:      info,
			State:     StatePending,
			CreatedAt: time.UnixMilli(row.CreatedAt),
			claimable: true,
			durable:   true,
		})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range loaded {
		s.events = append(s.events, e)
		s.touched[e.JobID] = now
	}
	if len(loaded) > 0 {
		s.signalLocked()
	}
	return nil
}

// Flush waits until every event change made before the call is saved.
// It is a no-op without persistence.
func (s *Store) Flush(ctx context.Context) error {
	if p := s.persist.Load(); p != nil {
		return p.flush(ctx)
	}
	return nil
}

// Close saves queued event changes, bounded by ctx, and stops
// persistence; later changes stay in memory only. It is a no-op
// without persistence.
func (s *Store) Close(ctx context.Context) {
	if p := s.persist.Load(); p != nil {
		p.close(ctx)
	}
}

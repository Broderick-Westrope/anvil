package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
)

const (
	jobHeartbeatInterval = 30 * time.Second
	jobSweepInterval     = time.Hour
	jobLogRetention      = 14 * 24 * time.Hour
	jobLogMaxTotalBytes  = 500 * 1024 * 1024
)

// jobLifecycle owns the persisted job store for this process: it
// registers the process as an Anvil instance, heartbeats so other
// processes know it is live, recovers jobs of instances that died, and
// prunes old logs.
type jobLifecycle struct {
	store  *jobstore.Store
	q      db.Querier
	logDir string
	now    func() time.Time

	heartbeatInterval time.Duration
	sweepInterval     time.Duration
	retention         time.Duration
	maxLogBytes       int64

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newJobLifecycle(q db.Querier, logDir string) (*jobLifecycle, error) {
	store, err := jobstore.New(q, logDir)
	if err != nil {
		return nil, err
	}
	return &jobLifecycle{
		store:             store,
		q:                 q,
		logDir:            logDir,
		now:               time.Now,
		heartbeatInterval: jobHeartbeatInterval,
		sweepInterval:     jobSweepInterval,
		retention:         jobLogRetention,
		maxLogBytes:       jobLogMaxTotalBytes,
	}, nil
}

// Start registers this instance, recovers jobs left running by dead
// instances, and starts the heartbeat and retention sweeper.
func (l *jobLifecycle) Start(ctx context.Context) error {
	now := l.now().UnixMilli()
	if err := l.q.UpsertAnvilInstance(ctx, db.UpsertAnvilInstanceParams{
		ID:          l.store.InstanceID(),
		Pid:         int64(os.Getpid()),
		StartedAt:   now,
		HeartbeatAt: now,
	}); err != nil {
		return fmt.Errorf("registering anvil instance: %w", err)
	}
	if err := l.recover(ctx); err != nil {
		slog.Warn("Failed to recover background jobs of exited Anvil processes", "error", err)
	}

	ctx, l.cancel = context.WithCancel(ctx)
	l.wg.Go(func() { l.heartbeat(ctx) })
	l.wg.Go(func() { l.sweepLoop(ctx) })
	return nil
}

// Stop cancels the heartbeat and sweeper and waits for them to return.
func (l *jobLifecycle) Stop() {
	if l.cancel != nil {
		l.cancel()
	}
	l.wg.Wait()
}

func (l *jobLifecycle) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(l.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.q.TouchAnvilInstance(ctx, db.TouchAnvilInstanceParams{
				HeartbeatAt: l.now().UnixMilli(),
				ID:          l.store.InstanceID(),
			}); err != nil && ctx.Err() == nil {
				slog.Warn("Failed to record Anvil instance heartbeat", "error", err)
			}
		}
	}
}

func (l *jobLifecycle) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(l.sweepInterval)
	defer ticker.Stop()
	for {
		if err := l.sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("Failed to prune background job logs", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// recover marks running jobs as interrupted when their instance's
// heartbeat is stale or the instance row is gone, and removes stale
// instance rows.
func (l *jobLifecycle) recover(ctx context.Context) error {
	now := l.now()
	instances, err := l.q.ListAnvilInstances(ctx)
	if err != nil {
		return fmt.Errorf("listing anvil instances: %w", err)
	}
	known := make(map[string]bool, len(instances))
	for _, inst := range instances {
		known[inst.ID] = true
		if inst.ID == l.store.InstanceID() {
			continue
		}
		heartbeat := time.UnixMilli(inst.HeartbeatAt)
		if now.Sub(heartbeat) < jobstore.InstanceLiveWindow {
			continue
		}
		if err := l.markInterrupted(ctx, inst.ID, heartbeat); err != nil {
			return err
		}
		if err := l.q.DeleteAnvilInstance(ctx, inst.ID); err != nil {
			return fmt.Errorf("deleting anvil instance %s: %w", inst.ID, err)
		}
	}

	running, err := l.q.ListRunningBackgroundJobs(ctx)
	if err != nil {
		return fmt.Errorf("listing running background jobs: %w", err)
	}
	orphaned := make(map[string]bool)
	for _, job := range running {
		if !known[job.InstanceID] {
			orphaned[job.InstanceID] = true
		}
	}
	for instanceID := range orphaned {
		if err := l.markInterrupted(ctx, instanceID, now); err != nil {
			return err
		}
	}
	return nil
}

func (l *jobLifecycle) markInterrupted(ctx context.Context, instanceID string, at time.Time) error {
	if err := l.q.MarkBackgroundJobsInterrupted(ctx, db.MarkBackgroundJobsInterruptedParams{
		CompletedAt: sql.NullInt64{Int64: at.UnixMilli(), Valid: true},
		InstanceID:  instanceID,
	}); err != nil {
		return fmt.Errorf("marking jobs of instance %s interrupted: %w", instanceID, err)
	}
	return nil
}

// sweep expires logs of jobs that completed more than the retention
// period ago, then the oldest logs while their total size exceeds the
// cap.
func (l *jobLifecycle) sweep(ctx context.Context) error {
	now := l.now()
	old, err := l.q.ListBackgroundJobsWithLogsBefore(ctx, sql.NullInt64{Int64: now.Add(-l.retention).UnixMilli(), Valid: true})
	if err != nil {
		return fmt.Errorf("listing old background job logs: %w", err)
	}
	for _, job := range old {
		if err := l.expire(ctx, job.ID, now); err != nil {
			return err
		}
	}

	logs, err := l.q.ListBackgroundJobLogsOldestFirst(ctx)
	if err != nil {
		return fmt.Errorf("listing background job logs: %w", err)
	}
	var total int64
	for _, log := range logs {
		total += log.LogBytes
	}
	for _, log := range logs {
		if total <= l.maxLogBytes {
			break
		}
		if err := l.expire(ctx, log.ID, now); err != nil {
			return err
		}
		total -= log.LogBytes
	}
	return nil
}

// expire deletes a job's log files and records when they were pruned.
func (l *jobLifecycle) expire(ctx context.Context, key int64, now time.Time) error {
	id := jobstore.FormatID(key)
	stdoutPath, stderrPath := jobstore.LogPaths(l.logDir, id)
	var removeErr error
	for _, path := range []string{stdoutPath, stderrPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErr = cmp.Or(removeErr, err)
		}
	}
	if removeErr != nil {
		return fmt.Errorf("removing logs of job %s: %w", id, removeErr)
	}
	if err := l.q.MarkBackgroundJobLogExpired(ctx, db.MarkBackgroundJobLogExpiredParams{
		LogExpiredAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true},
		ID:           key,
	}); err != nil {
		return fmt.Errorf("marking logs of job %s expired: %w", id, err)
	}
	return nil
}

// Close stops the heartbeat and sweeper, unregisters this instance,
// and closes the store, after which nothing reaches the database.
func (l *jobLifecycle) Close(ctx context.Context) {
	l.Stop()
	if err := l.q.DeleteAnvilInstance(ctx, l.store.InstanceID()); err != nil {
		slog.Warn("Failed to unregister Anvil instance", "error", err)
	}
	l.store.Close()
}

// Package jobstore persists published background jobs and their output
// logs so they survive eviction from memory and Anvil restarts.
package jobstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// ErrClosed is returned by every [Store] method after [Store.Close].
var ErrClosed = errors.New("job store closed")

// Store persists job records in the database and job output in log
// files under logDir.
type Store struct {
	q          db.Querier
	logDir     string
	instanceID string
	now        func() time.Time
	closed     atomic.Bool
}

// InstanceLiveWindow is how recent an instance's heartbeat must be for
// it to count as a running Anvil process.
const InstanceLiveWindow = 90 * time.Second

// Record is a persisted job plus its liveness as seen by this process.
type Record struct {
	Info           shell.JobInfo
	EndReason      string
	InstanceID     string
	Remote         bool // Running in another live Anvil process.
	ExitCodeKnown  bool // Info.ExitCode is meaningful.
	LogExpired     time.Time
	Truncated      bool
	PrePublishLost bool
	LogWriteError  string
}

// DefaultLogDir returns the production directory for job logs.
func DefaultLogDir() string {
	return filepath.Join(config.GlobalDataDir(), "jobs")
}

// FormatID renders a database key as a job ID.
func FormatID(key int64) string {
	return fmt.Sprintf("%03X", key)
}

// ParseID converts a job ID back to its database key. IDs that are not
// positive hex numbers, such as fallback IDs, report false.
func ParseID(id string) (int64, bool) {
	if id == "" || id[0] == '+' || id[0] == '-' {
		return 0, false
	}
	key, err := strconv.ParseInt(id, 16, 64)
	if err != nil || key <= 0 {
		return 0, false
	}
	return key, true
}

// LogPaths returns the stdout and stderr log file paths for a job ID.
func LogPaths(logDir, id string) (stdout, stderr string) {
	return filepath.Join(logDir, id+".stdout"), filepath.Join(logDir, id+".stderr")
}

// New creates a store with a random instance ID, creating logDir if
// needed.
func New(q db.Querier, logDir string) (*Store, error) {
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating job log directory: %w", err)
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("generating instance ID: %w", err)
	}
	return &Store{
		q:          q,
		logDir:     logDir,
		instanceID: hex.EncodeToString(b[:]),
		now:        time.Now,
	}, nil
}

// InstanceID returns the ID this process registers its jobs under.
func (s *Store) InstanceID() string {
	return s.instanceID
}

// Get returns the persisted record for a job ID. IDs that don't parse
// are reported as not found without touching the database.
func (s *Store) Get(ctx context.Context, id string) (Record, bool, error) {
	if s.closed.Load() {
		return Record{}, false, ErrClosed
	}
	key, ok := ParseID(id)
	if !ok {
		return Record{}, false, nil
	}
	row, err := s.q.GetBackgroundJob(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("getting background job %s: %w", id, err)
	}
	records := []Record{recordFromRow(row)}
	if err := s.applyLiveness(ctx, records); err != nil {
		return Record{}, false, err
	}
	return records[0], true, nil
}

// ListBySession returns a session's persisted jobs, running jobs first
// (oldest first), then finished jobs (newest first).
func (s *Store) ListBySession(ctx context.Context, sessionID string) ([]Record, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	rows, err := s.q.ListBackgroundJobsBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing background jobs: %w", err)
	}
	records := make([]Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, recordFromRow(row))
	}
	if err := s.applyLiveness(ctx, records); err != nil {
		return nil, err
	}
	slices.SortFunc(records, func(a, b Record) int { return shell.CompareJobs(a.Info, b.Info) })
	return records, nil
}

// applyLiveness resolves running records owned by other instances: a
// live instance makes them Remote, and a dead or missing one means
// they were interrupted, even if recovery has not marked them yet.
func (s *Store) applyLiveness(ctx context.Context, records []Record) error {
	if !slices.ContainsFunc(records, s.isForeignRunning) {
		return nil
	}
	instances, err := s.q.ListAnvilInstances(ctx)
	if err != nil {
		return fmt.Errorf("listing anvil instances: %w", err)
	}
	heartbeats := make(map[string]time.Time, len(instances))
	for _, inst := range instances {
		heartbeats[inst.ID] = time.UnixMilli(inst.HeartbeatAt)
	}
	now := s.now()
	for i := range records {
		rec := &records[i]
		if !s.isForeignRunning(*rec) {
			continue
		}
		hb, ok := heartbeats[rec.InstanceID]
		if ok && now.Sub(hb) < InstanceLiveWindow {
			rec.Remote = true
			continue
		}
		rec.EndReason = shell.EndInterrupted
		rec.Info.EndReason = shell.EndInterrupted
		rec.Info.Done = true
		rec.Info.CompletedAt = now
		if ok {
			rec.Info.CompletedAt = hb
		}
	}
	return nil
}

func (s *Store) isForeignRunning(rec Record) bool {
	return !rec.Info.Done && rec.InstanceID != s.instanceID
}

// ReadLog returns a persisted job's stored output.
func (s *Store) ReadLog(id string) (stdout, stderr []byte, err error) {
	if s.closed.Load() {
		return nil, nil, ErrClosed
	}
	key, ok := ParseID(id)
	if !ok {
		return nil, nil, fmt.Errorf("invalid job ID: %s", id)
	}
	stdoutPath, stderrPath := LogPaths(s.logDir, FormatID(key))
	if stdout, err = os.ReadFile(stdoutPath); err != nil {
		return nil, nil, fmt.Errorf("reading job stdout log: %w", err)
	}
	if stderr, err = os.ReadFile(stderrPath); err != nil {
		return nil, nil, fmt.Errorf("reading job stderr log: %w", err)
	}
	return stdout, stderr, nil
}

// Close makes every later method return [ErrClosed] without touching
// the database.
func (s *Store) Close() {
	s.closed.Store(true)
}

var _ shell.JobRecorder = (*Store)(nil)

// Allocate inserts a running record and opens its log files. If the
// files can't be opened, the record is deleted again so nothing is
// left behind, and the caller runs the job under a fallback ID.
func (s *Store) Allocate(ctx context.Context, req shell.AllocateRequest) (string, shell.JobLog, error) {
	if s.closed.Load() {
		return "", nil, ErrClosed
	}
	info := req.Info
	key, err := s.q.CreateBackgroundJob(ctx, db.CreateBackgroundJobParams{
		SessionID:         info.SessionID,
		Origin:            string(info.Origin),
		Command:           info.Command,
		Description:       info.Description,
		WorkingDir:        info.WorkingDir,
		StartedAt:         info.StartedAt.UnixMilli(),
		InstanceID:        s.instanceID,
		LogPrePublishLost: boolToInt(req.PrePublishLost),
	})
	if err != nil {
		return "", nil, fmt.Errorf("creating background job: %w", err)
	}

	id := FormatID(key)
	stdoutPath, stderrPath := LogPaths(s.logDir, id)
	log, err := shell.NewJobLog(stdoutPath, stderrPath)
	if err != nil {
		delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if delErr := s.q.DeleteBackgroundJob(delCtx, key); delErr != nil {
			err = errors.Join(err, fmt.Errorf("deleting background job %s: %w", id, delErr))
		}
		return "", nil, err
	}
	return id, log, nil
}

// Finalize records a job's end state. Only the first call for a job
// takes effect.
func (s *Store) Finalize(ctx context.Context, id string, info shell.JobInfo, endReason string, stats shell.LogStats) error {
	if s.closed.Load() {
		return ErrClosed
	}
	key, ok := ParseID(id)
	if !ok {
		return fmt.Errorf("invalid job ID: %s", id)
	}
	completedAt := s.now()
	var exitCode sql.NullInt64
	if info.Done {
		completedAt = info.CompletedAt
		if shell.ExitCodeMeaningful(endReason) {
			exitCode = sql.NullInt64{Int64: int64(info.ExitCode), Valid: true}
		}
	}
	if _, err := s.q.FinalizeBackgroundJob(ctx, db.FinalizeBackgroundJobParams{
		CompletedAt:   sql.NullInt64{Int64: completedAt.UnixMilli(), Valid: true},
		ExitCode:      exitCode,
		EndReason:     sql.NullString{String: endReason, Valid: true},
		LogBytes:      stats.Bytes,
		LogTruncated:  boolToInt(stats.Truncated),
		LogWriteError: stats.WriteError,
		ID:            key,
	}); err != nil {
		return fmt.Errorf("finalizing background job %s: %w", id, err)
	}
	return nil
}

// Transferred records that jobs now belong to toSession.
func (s *Store) Transferred(ctx context.Context, jobIDs []string, toSession string) error {
	if s.closed.Load() {
		return ErrClosed
	}
	keys := make([]int64, 0, len(jobIDs))
	for _, id := range jobIDs {
		if key, ok := ParseID(id); ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if err := s.q.TransferBackgroundJobs(ctx, db.TransferBackgroundJobsParams{
		SessionID: toSession,
		Ids:       keys,
	}); err != nil {
		return fmt.Errorf("transferring background jobs: %w", err)
	}
	return nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func recordFromRow(row db.BackgroundJob) Record {
	rec := Record{
		Info: shell.JobInfo{
			ID:          FormatID(row.ID),
			SessionID:   row.SessionID,
			Origin:      shell.JobOrigin(row.Origin),
			Command:     row.Command,
			Description: row.Description,
			WorkingDir:  row.WorkingDir,
			StartedAt:   time.UnixMilli(row.StartedAt),
			EndReason:   row.EndReason.String,
		},
		EndReason:      row.EndReason.String,
		InstanceID:     row.InstanceID,
		Truncated:      row.LogTruncated != 0,
		PrePublishLost: row.LogPrePublishLost != 0,
		LogWriteError:  row.LogWriteError,
	}
	if row.CompletedAt.Valid {
		rec.Info.Done = true
		rec.Info.CompletedAt = time.UnixMilli(row.CompletedAt.Int64)
		rec.Info.ExitCode = int(row.ExitCode.Int64)
		rec.ExitCodeKnown = row.ExitCode.Valid && shell.ExitCodeMeaningful(row.EndReason.String)
	}
	if row.LogExpiredAt.Valid {
		rec.LogExpired = time.UnixMilli(row.LogExpiredAt.Int64)
	}
	return rec
}

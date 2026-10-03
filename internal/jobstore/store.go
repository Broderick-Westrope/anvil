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

// Record is a persisted job plus its liveness as seen by this process.
type Record struct {
	Info           shell.JobInfo
	EndReason      string
	InstanceID     string
	Remote         bool // Running in another live Anvil process.
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
	return recordFromRow(row), true, nil
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
	slices.SortFunc(records, func(a, b Record) int { return shell.CompareJobs(a.Info, b.Info) })
	return records, nil
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
	}
	if row.LogExpiredAt.Valid {
		rec.LogExpired = time.UnixMilli(row.LogExpiredAt.Int64)
	}
	return rec
}

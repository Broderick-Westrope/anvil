// Package host persists workflow runs, their event logs and script
// approvals.
package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// Status is the state of a workflow run.
type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusAborted     Status = "aborted"
	StatusInterrupted Status = "interrupted"
)

// RunRecord is a persisted workflow run.
type RunRecord struct {
	ID                string
	Command           string // Command ID, e.g. project:build.
	CommandSource     string // "" for user commands, project or plugin:<name>.
	ScriptPath        string
	ScriptHash        string
	InvokingSessionID string
	Input             string // Raw argument string passed to main.
	WorkingDir        string // Canonical working directory.
	Status            Status
	Result            json.RawMessage
	Error             string
	Tmpdir            string
	InstanceID        string
	StartedAt         time.Time
	FinishedAt        time.Time // Zero while running.
}

// Store persists workflow state in the database and run directories
// under <dataDir>/workflows.
type Store struct {
	q          db.Querier
	dataDir    string
	instanceID string
	now        func() time.Time
}

// NewStore creates a store whose runs are owned by instanceID, the ID
// this process registers in anvil_instances.
func NewStore(q db.Querier, dataDir, instanceID string) *Store {
	return &Store{
		q:          q,
		dataDir:    dataDir,
		instanceID: instanceID,
		now:        time.Now,
	}
}

// RunDir returns the directory holding a run's files.
func (s *Store) RunDir(runID string) string {
	return filepath.Join(s.dataDir, "workflows", runID)
}

// Tmpdir returns the scratch directory scripts see as their tmpdir.
func (s *Store) Tmpdir(runID string) string {
	return filepath.Join(s.RunDir(runID), "tmp")
}

// StartRun records a new running run owned by this instance and creates
// its tmpdir. The record's Status, Tmpdir, InstanceID, Result, Error and
// FinishedAt are ignored. A zero StartedAt means now.
func (s *Store) StartRun(ctx context.Context, rec RunRecord) error {
	tmpdir := s.Tmpdir(rec.ID)
	if err := os.MkdirAll(tmpdir, 0o700); err != nil {
		return fmt.Errorf("create tmpdir of workflow run %s: %w", rec.ID, err)
	}
	started := rec.StartedAt
	if started.IsZero() {
		started = s.now()
	}
	if err := s.q.CreateWorkflowRun(ctx, db.CreateWorkflowRunParams{
		ID:                rec.ID,
		Command:           rec.Command,
		CommandSource:     rec.CommandSource,
		ScriptPath:        rec.ScriptPath,
		ScriptHash:        rec.ScriptHash,
		InvokingSessionID: rec.InvokingSessionID,
		Input:             rec.Input,
		WorkingDir:        rec.WorkingDir,
		Tmpdir:            tmpdir,
		InstanceID:        s.instanceID,
		StartedAt:         started.UnixMilli(),
	}); err != nil {
		return fmt.Errorf("create workflow run %s: %w", rec.ID, err)
	}
	return nil
}

// FinishRun records a running run's final status, the JSON main returned
// and any error message. It fails if the run doesn't exist or has
// already finished.
func (s *Store) FinishRun(ctx context.Context, id string, status Status, result json.RawMessage, errMsg string) error {
	n, err := s.q.FinishWorkflowRun(ctx, db.FinishWorkflowRunParams{
		Status:     string(status),
		Result:     nullJSON(result),
		Error:      errMsg,
		FinishedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true},
		ID:         id,
	})
	if err != nil {
		return fmt.Errorf("finish workflow run %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("finish workflow run %s: no running run with that ID", id)
	}
	return nil
}

// GetRun returns a persisted run, reporting false when it doesn't exist.
func (s *Store) GetRun(ctx context.Context, id string) (RunRecord, bool, error) {
	row, err := s.q.GetWorkflowRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, false, nil
	}
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("get workflow run %s: %w", id, err)
	}
	rec := RunRecord{
		ID:                row.ID,
		Command:           row.Command,
		CommandSource:     row.CommandSource,
		ScriptPath:        row.ScriptPath,
		ScriptHash:        row.ScriptHash,
		InvokingSessionID: row.InvokingSessionID,
		Input:             row.Input,
		WorkingDir:        row.WorkingDir,
		Status:            Status(row.Status),
		Result:            rawJSON(row.Result),
		Error:             row.Error,
		Tmpdir:            row.Tmpdir,
		InstanceID:        row.InstanceID,
		StartedAt:         time.UnixMilli(row.StartedAt),
	}
	if row.FinishedAt.Valid {
		rec.FinishedAt = time.UnixMilli(row.FinishedAt.Int64)
	}
	return rec, true, nil
}

// Recorder returns a [workflow.Recorder] that appends events to a run's
// log.
func (s *Store) Recorder(runID string) workflow.Recorder {
	return eventRecorder{q: s.q, runID: runID}
}

type eventRecorder struct {
	q     db.Querier
	runID string
}

func (r eventRecorder) Record(ctx context.Context, ev workflow.Event) error {
	if err := r.q.InsertWorkflowEvent(ctx, db.InsertWorkflowEventParams{
		RunID:      r.runID,
		Seq:        int64(ev.Seq),
		ParentSeq:  int64(ev.Parent),
		ChildIndex: int64(ev.Index),
		Primitive:  ev.Primitive,
		Phase:      string(ev.Phase),
		Args:       nullJSON(ev.Args),
		ArgsHash:   ev.ArgsHash,
		Result:     nullJSON(ev.Result),
		Error:      nullJSON(ev.Error),
		At:         ev.At.UnixMilli(),
	}); err != nil {
		return fmt.Errorf("record event %d of workflow run %s: %w", ev.Seq, r.runID, err)
	}
	return nil
}

// ListEvents returns a run's events ordered by sequence number, each
// call's events in the order they were recorded.
func (s *Store) ListEvents(ctx context.Context, runID string) ([]workflow.Event, error) {
	rows, err := s.q.ListWorkflowEvents(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list events of workflow run %s: %w", runID, err)
	}
	events := make([]workflow.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, workflow.Event{
			Seq:       int(row.Seq),
			Parent:    int(row.ParentSeq),
			Index:     int(row.ChildIndex),
			Primitive: row.Primitive,
			Phase:     workflow.Phase(row.Phase),
			Args:      rawJSON(row.Args),
			ArgsHash:  row.ArgsHash,
			Result:    rawJSON(row.Result),
			Error:     rawJSON(row.Error),
			At:        time.UnixMilli(row.At),
		})
	}
	return events, nil
}

func nullJSON(raw json.RawMessage) sql.NullString {
	if raw == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(raw), Valid: true}
}

func rawJSON(s sql.NullString) json.RawMessage {
	if !s.Valid {
		return nil
	}
	return json.RawMessage(s.String)
}

// Package host persists workflow runs, their event logs and script
// approvals.
package workflowhost

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
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
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
	// StatusDenied ends an invocation whose script wasn't approved. It never
	// has a run row.
	StatusDenied Status = "denied"
)

// Statuses returns every Status, so anything that describes them can check
// it describes them all.
func Statuses() []Status {
	return []Status{StatusRunning, StatusCompleted, StatusFailed, StatusAborted, StatusInterrupted, StatusDenied}
}

// RunRecord is a persisted workflow run.
type RunRecord struct {
	ID            string
	Command       string // Command ID, e.g. project:build.
	CommandSource string // "" for user commands, project or plugin:<name>.
	ScriptPath    string
	ScriptHash    string
	// BundleHash identifies the files the run read: the script and every
	// file approved with it.
	BundleHash        string
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
	// MarkerMessageID is the message marking the run in its invoking
	// session; empty until it's written.
	MarkerMessageID string
	// Paused reports whether the user paused the run.
	Paused bool
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
		BundleHash:        rec.BundleHash,
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

// HasRunningRunFrom reports whether a run started from sessionID is
// recorded as running, by this Anvil or another sharing the database.
func (s *Store) HasRunningRunFrom(ctx context.Context, sessionID string) (bool, error) {
	n, err := s.q.CountRunningWorkflowRunsFrom(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("count running workflow runs from %s: %w", sessionID, err)
	}
	return n > 0, nil
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
		BundleHash:        row.BundleHash,
		InvokingSessionID: row.InvokingSessionID,
		Input:             row.Input,
		WorkingDir:        row.WorkingDir,
		Status:            Status(row.Status),
		Result:            rawJSON(row.Result),
		Error:             row.Error,
		Tmpdir:            row.Tmpdir,
		InstanceID:        row.InstanceID,
		StartedAt:         time.UnixMilli(row.StartedAt),
		MarkerMessageID:   row.MarkerMessageID,
		Paused:            row.Paused != 0,
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

// SetMarker records the message that marks the run in its invoking
// session.
func (s *Store) SetMarker(ctx context.Context, runID, messageID string) error {
	if err := s.q.SetWorkflowRunMarker(ctx, db.SetWorkflowRunMarkerParams{ID: runID, MarkerMessageID: messageID}); err != nil {
		return fmt.Errorf("set marker of workflow run %s: %w", runID, err)
	}
	return nil
}

// SetPaused records whether the user has paused the run.
func (s *Store) SetPaused(ctx context.Context, runID string, paused bool) error {
	var v int64
	if paused {
		v = 1
	}
	if err := s.q.SetWorkflowRunPaused(ctx, db.SetWorkflowRunPausedParams{ID: runID, Paused: v}); err != nil {
		return fmt.Errorf("set paused of workflow run %s: %w", runID, err)
	}
	return nil
}

// listViewEvents returns a run's events with only the payloads its view
// reads: the arguments of status, agent and ask calls, the answer to each
// ask, and errors.
func (s *Store) listViewEvents(ctx context.Context, runID string) ([]workflow.Event, error) {
	rows, err := s.q.ListWorkflowViewEvents(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list view events of workflow run %s: %w", runID, err)
	}
	events := make([]workflow.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, workflow.Event{
			Seq:       int(row.Seq),
			Primitive: row.Primitive,
			Phase:     workflow.Phase(row.Phase),
			Args:      textJSON(row.Args),
			Result:    textJSON(row.Result),
			Error:     textJSON(row.Error),
		})
	}
	return events, nil
}

// textJSON is raw JSON stored as text, nil when empty.
func textJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
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

// approval returns the bundle last approved for a script, reporting false
// when it was never approved. A stored bundle that doesn't decode is
// returned empty, so every file shows as added and its hash never
// matches.
func (s *Store) approval(ctx context.Context, scriptPath string) (approved bundle, ok bool, err error) {
	row, err := s.q.GetWorkflowApproval(ctx, scriptPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get workflow approval of %s: %w", scriptPath, err)
	}
	if err := json.Unmarshal([]byte(row.Bundle), &approved); err != nil {
		return nil, true, nil
	}
	return approved, true, nil
}

// approve records the bundle approved for a script, replacing any earlier
// approval of the same path.
func (s *Store) approve(ctx context.Context, scriptPath string, approved bundle, origin string) error {
	encoded, err := json.Marshal(approved)
	if err != nil {
		return fmt.Errorf("encode workflow bundle of %s: %w", scriptPath, err)
	}
	if err := s.q.UpsertWorkflowApproval(ctx, db.UpsertWorkflowApprovalParams{
		ScriptPath: scriptPath,
		BundleHash: approved.hash(),
		Bundle:     string(encoded),
		Origin:     origin,
		ApprovedAt: s.now().UnixMilli(),
	}); err != nil {
		return fmt.Errorf("approve workflow %s: %w", scriptPath, err)
	}
	return nil
}

// MarkInterrupted marks running runs as interrupted when the Anvil
// instance that owns them is no longer live: its heartbeat is stale or
// its instance row is gone. Runs of this instance are left alone.
func (s *Store) MarkInterrupted(ctx context.Context) error {
	instances, err := s.q.ListAnvilInstances(ctx)
	if err != nil {
		return fmt.Errorf("list anvil instances: %w", err)
	}
	now := s.now()
	live := map[string]bool{s.instanceID: true}
	for _, inst := range instances {
		if now.Sub(time.UnixMilli(inst.HeartbeatAt)) < jobstore.InstanceLiveWindow {
			live[inst.ID] = true
		}
	}
	running, err := s.q.ListRunningWorkflowRuns(ctx)
	if err != nil {
		return fmt.Errorf("list running workflow runs: %w", err)
	}
	for _, run := range running {
		if live[run.InstanceID] {
			continue
		}
		if err := s.q.MarkWorkflowRunInterrupted(ctx, db.MarkWorkflowRunInterruptedParams{
			FinishedAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true},
			ID:         run.ID,
		}); err != nil {
			return fmt.Errorf("mark workflow run %s interrupted: %w", run.ID, err)
		}
	}
	return nil
}

// RunDirRetention is how long a finished run's directory is kept after
// the run started.
const RunDirRetention = 14 * 24 * time.Hour

// PruneRunDirs deletes the directories of runs that aren't running and
// started more than [RunDirRetention] before now, and directories with no
// run row whose mtime is that old. Run rows and event logs are kept. It
// carries on past a directory it can't prune and returns the joined
// errors.
func (s *Store) PruneRunDirs(ctx context.Context, now time.Time) error {
	root := filepath.Join(s.dataDir, "workflows")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read workflow run directories: %w", err)
	}
	cutoff := now.Add(-RunDirRetention)
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		run, ok, err := s.GetRun(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var expired bool
		if ok {
			expired = run.Status != StatusRunning && run.StartedAt.Before(cutoff)
		} else {
			info, err := entry.Info()
			if err != nil {
				errs = append(errs, fmt.Errorf("stat workflow run directory %s: %w", id, err))
				continue
			}
			expired = info.ModTime().Before(cutoff)
		}
		if !expired {
			continue
		}
		if err := os.RemoveAll(s.RunDir(id)); err != nil {
			errs = append(errs, fmt.Errorf("remove workflow run directory %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
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

package session

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/google/uuid"
	"github.com/zeebo/xxh3"
)

type TodoStatus string

const (
	TodoStatusPending    TodoStatus = "pending"
	TodoStatusInProgress TodoStatus = "in_progress"
	TodoStatusCompleted  TodoStatus = "completed"
)

// HashID returns the XXH3 hash of a session ID (UUID) as a hex string.
func HashID(id string) string {
	h := xxh3.New()
	h.WriteString(id)
	return fmt.Sprintf("%x", h.Sum(nil))
}

type Todo struct {
	Content    string     `json:"content"`
	Status     TodoStatus `json:"status"`
	ActiveForm string     `json:"active_form"`
}

// HasIncompleteTodos returns true if there are any non-completed todos.
func HasIncompleteTodos(todos []Todo) bool {
	for _, todo := range todos {
		if todo.Status != TodoStatusCompleted {
			return true
		}
	}
	return false
}

type Session struct {
	ID               string
	ParentSessionID  string
	Title            string
	TitleIsCustom    bool
	MessageCount     int64
	PromptTokens     int64
	CompletionTokens int64
	LeafMessageID    string
	WorkingDir       string
	EstimatedUsage   bool
	SummaryMessageID string
	Cost             float64
	Todos            []Todo
	Pinned           bool
	PinNote          string
	// WorkflowRunID is the workflow run that created the session, and
	// WorkflowSeq the seq of the run's event that created it. Both are zero
	// for sessions no run created.
	WorkflowRunID string
	WorkflowSeq   int
	// WorkflowKind is what a workflow run made the session for.
	WorkflowKind WorkflowKind
	// ForkedFrom is, for a fork, the message in the source session its
	// copy ends at.
	ForkedFrom string
	CreatedAt  int64
	UpdatedAt  int64
}

// WorkflowKind is what a workflow run made a session for.
type WorkflowKind string

const (
	// WorkflowKindSession is an orchestrator session the user can talk to,
	// such as a builder.
	WorkflowKindSession WorkflowKind = "session"
	// WorkflowKindAgent is a subagent's session.
	WorkflowKindAgent WorkflowKind = "agent"
)

// WorkflowOwner records which workflow run made a session, with the seq of
// the run's event that made it, what for, and for a fork, the source
// message its copy ends at.
type WorkflowOwner struct {
	RunID      string
	Seq        int
	Kind       WorkflowKind
	ForkedFrom string
}

// Conversational reports whether the user talks to this session directly:
// a top-level session, or an orchestrator session a workflow run made. A
// subagent's session isn't; its conversation is its parent's.
func (s Session) Conversational() bool {
	return s.ParentSessionID == "" || s.WorkflowKind == WorkflowKindSession
}

// MaxPinNoteLen is the maximum length of a pin note in runes.
const MaxPinNoteLen = 200

// sanitizePinNote flattens newlines to spaces, trims space, and caps the
// note at MaxPinNoteLen runes.
func sanitizePinNote(note string) string {
	note = strings.ReplaceAll(note, "\r\n", " ")
	note = strings.ReplaceAll(note, "\n", " ")
	note = strings.ReplaceAll(note, "\r", " ")
	note = strings.TrimSpace(note)
	runes := []rune(note)
	if len(runes) > MaxPinNoteLen {
		note = string(runes[:MaxPinNoteLen])
	}
	return note
}

type Service interface {
	pubsub.Subscriber[Session]
	Create(ctx context.Context, title, workingDir string) (Session, error)
	CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error)
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error)
	Get(ctx context.Context, id string) (Session, error)
	GetLast(ctx context.Context, workingDir string) (Session, error)
	GetLastGlobal(ctx context.Context) (Session, error)
	List(ctx context.Context, workingDir string) ([]Session, error)
	// Save writes the session's title, token counters and todos. It never
	// writes Cost: cost only grows, through AddCost, UpdateTitleAndUsage
	// and RollUpChildCost, so a stale copy can't undo cost another writer
	// added.
	Save(ctx context.Context, session Session) (Session, error)
	// AddCost adds delta to the session's cost.
	AddCost(ctx context.Context, id string, delta float64) error
	UpdateTitleAndUsage(ctx context.Context, sessionID, title string, titleIsCustom bool, promptTokens, completionTokens int64, cost float64) error
	Rename(ctx context.Context, id string, title string, titleIsCustom bool) error
	SetPin(ctx context.Context, id string, pinned bool, note string) error
	ListPinned(ctx context.Context) ([]Session, error)
	MoveLeaf(ctx context.Context, sessionID, leafMessageID string) error
	// Refresh publishes the session as stored, for changes made to it
	// outside this service, such as a message store moving its leaf.
	Refresh(ctx context.Context, sessionID string) error
	Delete(ctx context.Context, id string) error

	// CreateWorkflowSession creates a child session a workflow run owns,
	// recording its owner in the same write, so it's never seen without
	// one. An empty id gets a new one.
	CreateWorkflowSession(ctx context.Context, id, parentSessionID, title string, owner WorkflowOwner) (Session, error)
	// Discard deletes a session a workflow run made but couldn't finish
	// making, which nothing has used yet, so it skips Delete's checks that
	// the run is done with it.
	Discard(ctx context.Context, id string) error
	// WorkflowDescendants lists the sessions workflow runs made from id,
	// and from those, all the way down: what deleting id deletes with it.
	WorkflowDescendants(ctx context.Context, id string) ([]string, error)
	// SetWorkflowOwner records which workflow run made the session.
	SetWorkflowOwner(ctx context.Context, id string, owner WorkflowOwner) error
	// ListByWorkflowRun returns the sessions a workflow run created, in the
	// order its events created them.
	ListByWorkflowRun(ctx context.Context, runID string) ([]Session, error)

	// Agent tool session management
	CreateAgentToolSessionID(messageID, toolCallID string) string
	ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool)
	IsAgentToolSession(sessionID string) bool

	// Subagent identity and task call links.
	CreateSubagent(ctx context.Context, sub Subagent) (Subagent, error)
	GetSubagentByHandle(ctx context.Context, parentID, handle string) (Subagent, error)
	CreateSubagentCall(ctx context.Context, call SubagentCall) error
	GetSubagentCall(ctx context.Context, id string) (SubagentCall, error)
	FinishSubagentCall(ctx context.Context, id string) error
	RollUpChildCost(ctx context.Context, childID, callID string) error
}

type service struct {
	*pubsub.Broker[Session]
	db *sql.DB
	q  *db.Queries

	// Estimated usage stays in memory so fetch-modify-save paths (e.g.,
	// updating todos or parent-session cost) do not rebuild a session from
	// SQLite and incorrectly clear the UI "~" marker.
	estimatedUsageMu sync.RWMutex
	estimatedUsage   map[string]bool

	jobLogDir string
}

func (s *service) Create(ctx context.Context, title, workingDir string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:         uuid.New().String(),
		Title:      title,
		WorkingDir: workingDir,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error) {
	// Inherit working_dir from the parent session.
	parent, err := s.q.GetSessionByID(ctx, parentSessionID)
	if err != nil {
		return Session{}, fmt.Errorf("looking up parent session: %w", err)
	}
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              toolCallID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           title,
		WorkingDir:      parent.WorkingDir,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error) {
	// Inherit working_dir from the parent session.
	parent, err := s.q.GetSessionByID(ctx, parentSessionID)
	if err != nil {
		return Session{}, fmt.Errorf("looking up parent session: %w", err)
	}
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              "title-" + parentSessionID,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           "Generate a title",
		WorkingDir:      parent.WorkingDir,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

// Deleting a session deletes the sessions workflow runs made from it, so
// it's refused while they're in use, whichever Anvil is using them.
var (
	// ErrWorkflowRunning refuses it while a run started from the session,
	// or one that made it, is recorded as running.
	ErrWorkflowRunning = errors.New("a workflow using this session is still running; abort it (or wait for it to finish) before deleting the session")
	// ErrWorkflowSessionInUse refuses it while a session a run made from
	// it, such as a builder after hand-over, is working or has background
	// jobs running.
	ErrWorkflowSessionInUse = errors.New("a workflow session made from this session is working or has background jobs running; stop them before deleting it")
)

func (s *service) Discard(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	deleted, jobKeys, err := deleteSessionTree(ctx, s.q.WithTx(tx), id)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	s.finishDelete(deleted, jobKeys)
	return nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := s.q.WithTx(tx)
	// A session a running run made is still in use by it.
	own, err := qtx.GetSessionByID(ctx, id)
	if err != nil {
		return err
	}
	if own.WorkflowRunID != "" {
		run, err := qtx.GetWorkflowRun(ctx, own.WorkflowRunID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("checking the workflow run of %s: %w", id, err)
		}
		if err == nil && run.Status == "running" {
			return ErrWorkflowRunning
		}
	}
	deleted, jobKeys, err := deleteSessionTree(ctx, qtx, id)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	s.finishDelete(deleted, jobKeys)
	return nil
}

// finishDelete cleans up after deleted sessions once their rows are gone.
func (s *service) finishDelete(deleted []db.Session, jobKeys []int64) {
	s.removeJobLogs(jobKeys)
	for _, dbSession := range deleted {
		s.clearEstimatedUsageState(dbSession.ID)
		s.Publish(pubsub.DeletedEvent, s.fromDBItem(dbSession))
	}
}

// deleteSessionTree deletes a session with its messages and background
// jobs, and first the sessions workflow runs made from it (builders, which
// hold a copy of its conversation, and subagents), which are reached only
// through it. It returns what it deleted, children first, and the deleted
// jobs' log keys.
func deleteSessionTree(ctx context.Context, qtx *db.Queries, id string) ([]db.Session, []int64, error) {
	dbSession, err := qtx.GetSessionByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	// A run started from any session in the tree, such as a builder the
	// user continued and ran another workflow from, still uses it.
	running, err := qtx.CountRunningWorkflowRunsFrom(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("checking for running workflows from %s: %w", id, err)
	}
	if running > 0 {
		return nil, nil, ErrWorkflowRunning
	}
	children, err := qtx.ListWorkflowChildSessionIDs(ctx, sql.NullString{String: id, Valid: true})
	if err != nil {
		return nil, nil, fmt.Errorf("listing workflow sessions of %s: %w", id, err)
	}
	var deleted []db.Session
	var jobKeys []int64
	for _, child := range children {
		jobs, err := qtx.CountRunningBackgroundJobsBySession(ctx, child)
		if err != nil {
			return nil, nil, fmt.Errorf("checking jobs of workflow session %s: %w", child, err)
		}
		if jobs > 0 {
			return nil, nil, ErrWorkflowSessionInUse
		}
		childDeleted, childJobs, err := deleteSessionTree(ctx, qtx, child)
		if err != nil {
			return nil, nil, err
		}
		deleted = append(deleted, childDeleted...)
		jobKeys = append(jobKeys, childJobs...)
	}
	if err = qtx.DeleteSessionMessages(ctx, dbSession.ID); err != nil {
		return nil, nil, fmt.Errorf("deleting session messages: %w", err)
	}
	keys, err := qtx.ListBackgroundJobIDsBySession(ctx, dbSession.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing session background jobs: %w", err)
	}
	if err = qtx.DeleteBackgroundJobsBySession(ctx, dbSession.ID); err != nil {
		return nil, nil, fmt.Errorf("deleting session background jobs: %w", err)
	}
	if err = qtx.DeleteSession(ctx, dbSession.ID); err != nil {
		return nil, nil, fmt.Errorf("deleting session: %w", err)
	}
	return append(deleted, dbSession), append(jobKeys, keys...), nil
}

func (s *service) Get(ctx context.Context, id string) (Session, error) {
	dbSession, err := s.q.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.applyEstimatedUsageState(&session)
	return session, nil
}

func (s *service) GetLast(ctx context.Context, workingDir string) (Session, error) {
	dbSession, err := s.q.GetLastSessionByWorkingDir(ctx, workingDir)
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.applyEstimatedUsageState(&session)
	return session, nil
}

func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	todosJSON, err := marshalTodos(session.Todos)
	if err != nil {
		return Session{}, err
	}

	dbSession, err := s.q.UpdateSession(ctx, db.UpdateSessionParams{
		ID:               session.ID,
		Title:            session.Title,
		TitleIsCustom:    boolToInt64(session.TitleIsCustom),
		PromptTokens:     session.PromptTokens,
		CompletionTokens: session.CompletionTokens,
		Todos: sql.NullString{
			String: todosJSON,
			Valid:  todosJSON != "",
		},
	})
	if err != nil {
		return Session{}, err
	}
	estimatedUsage := session.EstimatedUsage
	s.setEstimatedUsageState(session.ID, estimatedUsage)
	session = s.fromDBItem(dbSession)
	session.EstimatedUsage = estimatedUsage
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

// validCostDelta rejects a delta that would shrink cost or leave it
// unrepairable, since cost only grows.
func validCostDelta(delta float64) error {
	if delta < 0 || math.IsNaN(delta) || math.IsInf(delta, 0) {
		return fmt.Errorf("cost can't change by %v", delta)
	}
	return nil
}

// AddCost adds delta to the session's cost and publishes the update.
func (s *service) AddCost(ctx context.Context, id string, delta float64) error {
	if err := validCostDelta(delta); err != nil {
		return fmt.Errorf("add cost to session %s: %w", id, err)
	}
	if delta == 0 {
		return nil
	}
	n, err := s.q.AddSessionCost(ctx, db.AddSessionCostParams{Delta: delta, ID: id})
	if err != nil {
		return fmt.Errorf("add cost to session %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("add cost to session %s: %w", id, sql.ErrNoRows)
	}
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return nil
}

// UpdateTitleAndUsage updates only the title and usage fields atomically.
// This is safer than fetching, modifying, and saving the entire session.
func (s *service) UpdateTitleAndUsage(ctx context.Context, sessionID, title string, titleIsCustom bool, promptTokens, completionTokens int64, cost float64) error {
	if err := validCostDelta(cost); err != nil {
		return fmt.Errorf("update title and usage of session %s: %w", sessionID, err)
	}
	err := s.q.UpdateSessionTitleAndUsage(ctx, db.UpdateSessionTitleAndUsageParams{
		ID:               sessionID,
		Title:            title,
		TitleIsCustom:    boolToInt64(titleIsCustom),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Cost:             cost,
	})
	if err != nil {
		return err
	}
	session, err := s.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return nil
}

// MoveLeaf updates the leaf_message_id for a session and publishes an
// UpdatedEvent so subscribers see the change.
func (s *service) MoveLeaf(ctx context.Context, sessionID, leafMessageID string) error {
	err := s.q.UpdateSessionLeaf(ctx, db.UpdateSessionLeafParams{
		ID:     sessionID,
		LeafID: sql.NullString{String: leafMessageID, Valid: leafMessageID != ""},
	})
	if err != nil {
		return fmt.Errorf("updating session leaf: %w", err)
	}
	session, err := s.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return nil
}

func (s *service) Refresh(ctx context.Context, sessionID string) error {
	session, err := s.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return nil
}

// Rename updates only the title of a session without touching updated_at or
// usage fields.
func (s *service) Rename(ctx context.Context, id string, title string, titleIsCustom bool) error {
	err := s.q.RenameSession(ctx, db.RenameSessionParams{
		ID:            id,
		Title:         title,
		TitleIsCustom: boolToInt64(titleIsCustom),
	})
	if err != nil {
		return err
	}
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return nil
}

// SetPin updates only the pin state and note of a session without touching
// updated_at or usage fields. The note is sanitized to a single line capped
// at MaxPinNoteLen runes; when unpinning, the note is cleared.
func (s *service) SetPin(ctx context.Context, id string, pinned bool, note string) error {
	if pinned {
		note = sanitizePinNote(note)
	} else {
		note = ""
	}
	err := s.q.SetSessionPin(ctx, db.SetSessionPinParams{
		ID:      id,
		Pinned:  boolToInt64(pinned),
		PinNote: note,
	})
	if err != nil {
		return err
	}
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return nil
}

// ListPinned returns all pinned top-level sessions across working dirs,
// newest first.
func (s *service) ListPinned(ctx context.Context) ([]Session, error) {
	dbSessions, err := s.q.ListPinnedSessions(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
		s.applyEstimatedUsageState(&sessions[i])
	}
	return sessions, nil
}

func (s *service) GetLastGlobal(ctx context.Context) (Session, error) {
	dbSession, err := s.q.GetLastGlobalSession(ctx)
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.applyEstimatedUsageState(&session)
	return session, nil
}

func (s *service) List(ctx context.Context, workingDir string) ([]Session, error) {
	var dbSessions []db.Session
	var err error
	if workingDir != "" {
		dbSessions, err = s.q.ListSessionsByWorkingDir(ctx, workingDir)
	} else {
		dbSessions, err = s.q.ListAllSessions(ctx)
	}
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
		s.applyEstimatedUsageState(&sessions[i])
	}
	return sessions, nil
}

func (s *service) applyEstimatedUsageState(session *Session) {
	s.estimatedUsageMu.RLock()
	session.EstimatedUsage = s.estimatedUsage[session.ID]
	s.estimatedUsageMu.RUnlock()
}

func (s *service) setEstimatedUsageState(sessionID string, estimatedUsage bool) {
	s.estimatedUsageMu.Lock()
	defer s.estimatedUsageMu.Unlock()
	if estimatedUsage {
		s.estimatedUsage[sessionID] = true
		return
	}
	delete(s.estimatedUsage, sessionID)
}

func (s *service) clearEstimatedUsageState(sessionID string) {
	s.estimatedUsageMu.Lock()
	delete(s.estimatedUsage, sessionID)
	s.estimatedUsageMu.Unlock()
}

func (s *service) fromDBItem(item db.Session) Session {
	todos, err := unmarshalTodos(item.Todos.String)
	if err != nil {
		slog.Error("Failed to unmarshal todos", "session_id", item.ID, "error", err)
	}
	return Session{
		ID:               item.ID,
		ParentSessionID:  item.ParentSessionID.String,
		Title:            item.Title,
		TitleIsCustom:    item.TitleIsCustom != 0,
		MessageCount:     item.MessageCount,
		PromptTokens:     item.PromptTokens,
		CompletionTokens: item.CompletionTokens,
		LeafMessageID:    item.LeafMessageID.String,
		WorkingDir:       item.WorkingDir,
		Cost:             item.Cost,
		Todos:            todos,
		Pinned:           item.Pinned != 0,
		PinNote:          item.PinNote,
		WorkflowRunID:    item.WorkflowRunID,
		WorkflowSeq:      int(item.WorkflowSeq),
		WorkflowKind:     WorkflowKind(item.WorkflowKind),
		ForkedFrom:       item.ForkedFromMessageID,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

func (s *service) CreateWorkflowSession(ctx context.Context, id, parentSessionID, title string, owner WorkflowOwner) (Session, error) {
	parent, err := s.q.GetSessionByID(ctx, parentSessionID)
	if err != nil {
		return Session{}, fmt.Errorf("looking up parent session: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	qtx := s.q.WithTx(tx)
	if id == "" {
		id = uuid.NewString()
	}
	if _, err := qtx.CreateSession(ctx, db.CreateSessionParams{
		ID:              id,
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		Title:           title,
		WorkingDir:      parent.WorkingDir,
	}); err != nil {
		return Session{}, err
	}
	if err := qtx.SetSessionWorkflowOwner(ctx, db.SetSessionWorkflowOwnerParams{
		ID:                  id,
		WorkflowRunID:       owner.RunID,
		WorkflowSeq:         int64(owner.Seq),
		WorkflowKind:        string(owner.Kind),
		ForkedFromMessageID: owner.ForkedFrom,
	}); err != nil {
		return Session{}, fmt.Errorf("set workflow owner of session %s: %w", id, err)
	}
	dbSession, err := qtx.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("committing transaction: %w", err)
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) WorkflowDescendants(ctx context.Context, id string) ([]string, error) {
	var out []string
	queue := []string{id}
	for len(queue) > 0 {
		children, err := s.q.ListWorkflowChildSessionIDs(ctx, sql.NullString{String: queue[0], Valid: true})
		if err != nil {
			return nil, fmt.Errorf("listing workflow sessions of %s: %w", queue[0], err)
		}
		queue = append(queue[1:], children...)
		out = append(out, children...)
	}
	return out, nil
}

func (s *service) SetWorkflowOwner(ctx context.Context, id string, owner WorkflowOwner) error {
	if err := s.q.SetSessionWorkflowOwner(ctx, db.SetSessionWorkflowOwnerParams{
		ID:                  id,
		WorkflowRunID:       owner.RunID,
		WorkflowSeq:         int64(owner.Seq),
		WorkflowKind:        string(owner.Kind),
		ForkedFromMessageID: owner.ForkedFrom,
	}); err != nil {
		return fmt.Errorf("set workflow owner of session %s: %w", id, err)
	}
	return nil
}

func (s *service) ListByWorkflowRun(ctx context.Context, runID string) ([]Session, error) {
	rows, err := s.q.ListSessionsByWorkflowRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list sessions of workflow run %s: %w", runID, err)
	}
	sessions := make([]Session, len(rows))
	for i, row := range rows {
		sessions[i] = s.fromDBItem(row)
	}
	return sessions, nil
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func marshalTodos(todos []Todo) (string, error) {
	if len(todos) == 0 {
		return "", nil
	}
	data, err := json.Marshal(todos)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalTodos(data string) ([]Todo, error) {
	if data == "" {
		return []Todo{}, nil
	}
	var todos []Todo
	if err := json.Unmarshal([]byte(data), &todos); err != nil {
		return []Todo{}, err
	}
	return todos, nil
}

// Option configures a session service.
type Option func(*service)

// WithJobLogDir sets the directory holding background job logs, which
// are removed with their session. It defaults to
// [jobstore.DefaultLogDir].
func WithJobLogDir(dir string) Option {
	return func(s *service) { s.jobLogDir = dir }
}

func NewService(q *db.Queries, conn *sql.DB, opts ...Option) Service {
	broker := pubsub.NewBroker[Session]()
	s := &service{
		Broker:         broker,
		db:             conn,
		q:              q,
		estimatedUsage: make(map[string]bool),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// removeJobLogs deletes the log files of deleted background jobs. It is
// best effort: failures are logged once.
func (s *service) removeJobLogs(keys []int64) {
	if len(keys) == 0 {
		return
	}
	dir := cmp.Or(s.jobLogDir, jobstore.DefaultLogDir())
	var firstErr error
	for _, key := range keys {
		stdoutPath, stderrPath := jobstore.LogPaths(dir, jobstore.FormatID(key))
		for _, path := range []string{stdoutPath, stderrPath} {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		slog.Warn("Failed to remove background job logs", "error", firstErr)
	}
}

// AgentToolSessionID is the ID of the child session an agent tool call
// runs in: "messageID$$toolCallID".
func AgentToolSessionID(messageID, toolCallID string) string {
	return fmt.Sprintf("%s$$%s", messageID, toolCallID)
}

// ParseAgentToolSessionID splits an ID made by AgentToolSessionID. ok is
// false for any other ID.
func ParseAgentToolSessionID(id string) (messageID, toolCallID string, ok bool) {
	parts := strings.Split(id, "$$")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// CreateAgentToolSessionID creates a session ID for agent tool sessions using the format "messageID$$toolCallID"
func (s *service) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return AgentToolSessionID(messageID, toolCallID)
}

// ParseAgentToolSessionID parses an agent tool session ID into its components
func (s *service) ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool) {
	return ParseAgentToolSessionID(sessionID)
}

// IsAgentToolSession checks if a session ID follows the agent tool session format
func (s *service) IsAgentToolSession(sessionID string) bool {
	_, _, ok := s.ParseAgentToolSessionID(sessionID)
	return ok
}

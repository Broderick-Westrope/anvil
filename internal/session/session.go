package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/Broderick-Westrope/anvil/internal/db"
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
	CreatedAt        int64
	UpdatedAt        int64
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
	Save(ctx context.Context, session Session) (Session, error)
	UpdateTitleAndUsage(ctx context.Context, sessionID, title string, titleIsCustom bool, promptTokens, completionTokens int64, cost float64) error
	Rename(ctx context.Context, id string, title string, titleIsCustom bool) error
	SetPin(ctx context.Context, id string, pinned bool, note string) error
	ListPinned(ctx context.Context) ([]Session, error)
	MoveLeaf(ctx context.Context, sessionID, leafMessageID string) error
	Delete(ctx context.Context, id string) error

	// Agent tool session management
	CreateAgentToolSessionID(messageID, toolCallID string) string
	ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool)
	IsAgentToolSession(sessionID string) bool
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

func (s *service) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := s.q.WithTx(tx)

	dbSession, err := qtx.GetSessionByID(ctx, id)
	if err != nil {
		return err
	}
	if err = qtx.DeleteSessionMessages(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session messages: %w", err)
	}
	if err = qtx.DeleteSessionFiles(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session files: %w", err)
	}
	if err = qtx.DeleteSession(ctx, dbSession.ID); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	session := s.fromDBItem(dbSession)
	s.clearEstimatedUsageState(dbSession.ID)
	s.Publish(pubsub.DeletedEvent, session)
	return nil
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
		Cost:             session.Cost,
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

// UpdateTitleAndUsage updates only the title and usage fields atomically.
// This is safer than fetching, modifying, and saving the entire session.
func (s *service) UpdateTitleAndUsage(ctx context.Context, sessionID, title string, titleIsCustom bool, promptTokens, completionTokens int64, cost float64) error {
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
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
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

func NewService(q *db.Queries, conn *sql.DB) Service {
	broker := pubsub.NewBroker[Session]()
	return &service{
		Broker:         broker,
		db:             conn,
		q:              q,
		estimatedUsage: make(map[string]bool),
	}
}

// CreateAgentToolSessionID creates a session ID for agent tool sessions using the format "messageID$$toolCallID"
func (s *service) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return fmt.Sprintf("%s$$%s", messageID, toolCallID)
}

// ParseAgentToolSessionID parses an agent tool session ID into its components
func (s *service) ParseAgentToolSessionID(sessionID string) (messageID string, toolCallID string, ok bool) {
	parts := strings.Split(sessionID, "$$")
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// IsAgentToolSession checks if a session ID follows the agent tool session format
func (s *service) IsAgentToolSession(sessionID string) bool {
	_, _, ok := s.ParseAgentToolSessionID(sessionID)
	return ok
}

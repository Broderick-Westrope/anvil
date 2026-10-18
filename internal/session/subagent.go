package session

import (
	"context"
	"fmt"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
)

// SubagentOrigin says what spawned a subagent.
type SubagentOrigin string

// SubagentOriginTask is a subagent spawned by the task tool.
const SubagentOriginTask SubagentOrigin = "task"

// Subagent is the stored identity of a subagent's child session: what it
// is called and what it ran as, so it can be resumed the same way.
type Subagent struct {
	SessionID       string
	ParentSessionID string
	// Handle names the subagent to its parent. It is unique among the
	// parent's subagents.
	Handle    string
	Origin    SubagentOrigin
	AgentType string
	Depth     int
	Provider  string
	Model     string
}

// SubagentCallKind says whether a task call started a subagent or resumed
// one.
type SubagentCallKind string

const (
	SubagentCallNew    SubagentCallKind = "new"
	SubagentCallResume SubagentCallKind = "resume"
)

// SubagentCall links one task call to the child session it ran in.
type SubagentCall struct {
	// ID is the call's agent tool session ID (messageID$$toolCallID).
	ID        string
	SessionID string
	Kind      SubagentCallKind
	// Cost is what the child spent during this call. CreatedAt and
	// FinishedAt are Unix seconds; FinishedAt is 0 while it runs.
	Cost       float64
	CreatedAt  int64
	FinishedAt int64
}

// minHandleSuffix is the shortest hex suffix a handle gets; a suffix that
// collides with a sibling's is lengthened one digit at a time.
const minHandleSuffix = 4

// CreateSubagent stores s with a handle of the form <type>-<hex suffix>,
// derived from the child session ID. Any Handle on s is ignored.
func (s *service) CreateSubagent(ctx context.Context, sub Subagent) (Subagent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Subagent{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	qtx := s.q.WithTx(tx)

	// Take the shortest free suffix, adding one hash digit per collision.
	hash := HashID(sub.SessionID)
	suffix, rest := hash[:minHandleSuffix], hash[minHandleSuffix:]
	for {
		candidate := sub.AgentType + "-" + suffix
		taken, err := qtx.SubagentHandleExists(ctx, db.SubagentHandleExistsParams{ParentSessionID: sub.ParentSessionID, Handle: candidate})
		if err != nil {
			return Subagent{}, fmt.Errorf("check subagent handle %s: %w", candidate, err)
		}
		if taken == 0 {
			sub.Handle = candidate
			break
		}
		if rest == "" {
			return Subagent{}, fmt.Errorf("no free handle for subagent %s", sub.SessionID)
		}
		suffix, rest = suffix+rest[:1], rest[1:]
	}
	if err := qtx.CreateSubagent(ctx, db.CreateSubagentParams{
		SessionID:       sub.SessionID,
		ParentSessionID: sub.ParentSessionID,
		Handle:          sub.Handle,
		Origin:          string(sub.Origin),
		AgentType:       sub.AgentType,
		Depth:           int64(sub.Depth),
		Provider:        sub.Provider,
		Model:           sub.Model,
	}); err != nil {
		return Subagent{}, fmt.Errorf("create subagent %s: %w", sub.SessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return Subagent{}, fmt.Errorf("commit subagent %s: %w", sub.SessionID, err)
	}
	return sub, nil
}

// GetSubagentByHandle returns the direct child of parentID called handle.
func (s *service) GetSubagentByHandle(ctx context.Context, parentID, handle string) (Subagent, error) {
	row, err := s.q.GetSubagentByHandle(ctx, db.GetSubagentByHandleParams{ParentSessionID: parentID, Handle: handle})
	if err != nil {
		return Subagent{}, err
	}
	return subagentFromDB(row), nil
}

func subagentFromDB(row db.Subagent) Subagent {
	return Subagent{
		SessionID:       row.SessionID,
		ParentSessionID: row.ParentSessionID,
		Handle:          row.Handle,
		Origin:          SubagentOrigin(row.Origin),
		AgentType:       row.AgentType,
		Depth:           int(row.Depth),
		Provider:        row.Provider,
		Model:           row.Model,
	}
}

// CreateSubagentCall stores the link between a task call and its child
// session.
func (s *service) CreateSubagentCall(ctx context.Context, call SubagentCall) error {
	return s.q.CreateSubagentCall(ctx, db.CreateSubagentCallParams{ID: call.ID, SessionID: call.SessionID, Kind: string(call.Kind)})
}

// GetSubagentCall returns the link stored for the task call id.
func (s *service) GetSubagentCall(ctx context.Context, id string) (SubagentCall, error) {
	row, err := s.q.GetSubagentCall(ctx, id)
	if err != nil {
		return SubagentCall{}, err
	}
	return SubagentCall{
		ID: row.ID, SessionID: row.SessionID, Kind: SubagentCallKind(row.Kind),
		Cost: row.Cost, CreatedAt: row.CreatedAt, FinishedAt: row.FinishedAt.Int64,
	}, nil
}

// FinishSubagentCall records that the task call id has returned.
func (s *service) FinishSubagentCall(ctx context.Context, id string) error {
	return s.q.FinishSubagentCall(ctx, id)
}

// RollUpChildCost adds to its parent, and to the task call callID when it
// isn't "", the cost childID incurred since the last rollup. It's atomic,
// so concurrent rollups of siblings, and repeated rollups of one child,
// never lose or double count cost.
func (s *service) RollUpChildCost(ctx context.Context, childID, callID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	qtx := s.q.WithTx(tx)
	child, err := qtx.GetSessionByID(ctx, childID)
	if err != nil {
		return fmt.Errorf("get child session %s: %w", childID, err)
	}
	delta := child.Cost - child.CostRolledUp
	if err := validCostDelta(delta); err != nil {
		return fmt.Errorf("roll up cost of %s: %w", childID, err)
	}
	if delta == 0 {
		// Nothing new: writing anything would only bump updated_at.
		return nil
	}
	rolled, err := qtx.AddSessionCost(ctx, db.AddSessionCostParams{Delta: delta, ID: child.ParentSessionID.String})
	if err != nil {
		return fmt.Errorf("add cost of %s to parent: %w", childID, err)
	}
	if rolled == 0 {
		return fmt.Errorf("add cost of %s to parent: parent session not found", childID)
	}
	if callID != "" {
		if err := qtx.AddSubagentCallCost(ctx, db.AddSubagentCallCostParams{Delta: delta, ID: callID}); err != nil {
			return fmt.Errorf("add cost of %s to call %s: %w", childID, callID, err)
		}
	}
	if err := qtx.MarkChildCostRolledUp(ctx, childID); err != nil {
		return fmt.Errorf("mark cost of %s rolled up: %w", childID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cost rollup of %s: %w", childID, err)
	}
	if child.ParentSessionID.Valid {
		if parent, err := s.Get(ctx, child.ParentSessionID.String); err == nil {
			s.Publish(pubsub.UpdatedEvent, parent)
		}
	}
	return nil
}

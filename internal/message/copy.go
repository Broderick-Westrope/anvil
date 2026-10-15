package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/db"
)

// CopyBranch copies the branch ending at leafID into the session toSession,
// root first, in one transaction, and makes the copy's tip toSession's
// leaf. Copies keep their content and times but get new IDs, and IDs that
// parts refer to (a compaction's first kept entry, a branch summary's
// origin) follow them. An exchange the branch ends in the middle of (an
// unfinished reply, or tool calls without results) is left out, so the
// copy never starts mid-exchange. It returns the copy's leaf, or "" when
// nothing was copied.
func (s *service) CopyBranch(ctx context.Context, leafID, toSession string) (string, error) {
	if leafID == "" {
		return "", nil
	}
	rows, err := s.q.GetBranchPath(ctx, leafID)
	if err != nil {
		return "", fmt.Errorf("read branch ending at %s: %w", leafID, err)
	}
	path := make([]db.Message, len(rows))
	for i, row := range rows {
		path[i] = db.Message(row)
	}
	path, err = s.trimUnfinishedExchange(path)
	if err != nil {
		return "", err
	}

	ids := make(map[string]string, len(path))
	for _, m := range path {
		ids[m.ID] = uuid.NewString()
	}
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin copy: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	qtx := db.New(tx)
	parent := ""
	for _, m := range path {
		parts, err := remapPartIDs(m.Parts, ids)
		if err != nil {
			return "", fmt.Errorf("copy message %s: %w", m.ID, err)
		}
		if err := qtx.CopyMessage(ctx, db.CopyMessageParams{
			ID:              ids[m.ID],
			SessionID:       toSession,
			Role:            m.Role,
			Parts:           parts,
			Model:           m.Model,
			Provider:        m.Provider,
			ParentMessageID: sql.NullString{String: parent, Valid: parent != ""},
			MessageType:     m.MessageType,
			CreatedAt:       m.CreatedAt,
			UpdatedAt:       m.UpdatedAt,
			FinishedAt:      m.FinishedAt,
		}); err != nil {
			return "", fmt.Errorf("copy message %s: %w", m.ID, err)
		}
		parent = ids[m.ID]
	}
	if err := qtx.UpdateSessionLeaf(ctx, db.UpdateSessionLeafParams{
		ID:     toSession,
		LeafID: sql.NullString{String: parent, Valid: parent != ""},
	}); err != nil {
		return "", fmt.Errorf("set leaf of copy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit copy: %w", err)
	}
	return parent, nil
}

// trimUnfinishedExchange drops the end of path back to the last message a
// conversation can carry on from: not a tool result waiting for its reply,
// and not a reply that's unfinished or ends in tool calls.
func (s *service) trimUnfinishedExchange(path []db.Message) ([]db.Message, error) {
	for len(path) > 0 {
		m, err := s.fromDBItem(path[len(path)-1])
		if err != nil {
			return nil, err
		}
		midExchange := m.Role == Tool ||
			(m.Role == Assistant && (!m.IsFinished() || len(m.ToolCalls()) > 0))
		if !midExchange {
			break
		}
		path = path[:len(path)-1]
	}
	return path, nil
}

// remapPartIDs rewrites message IDs that parts refer to. Only the parts
// that refer to messages are decoded; every other part, including ones
// this version doesn't know, is copied byte for byte.
func remapPartIDs(raw string, ids map[string]string) (string, error) {
	var wrapped []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		return "", err
	}
	changed := false
	for i, w := range wrapped {
		var part struct {
			Type partType        `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w, &part); err != nil {
			return "", err
		}
		var remapped ContentPart
		switch part.Type {
		case compactionType:
			var c CompactionContent
			if err := json.Unmarshal(part.Data, &c); err != nil {
				return "", err
			}
			to, ok := ids[c.FirstKeptEntryID]
			if !ok {
				continue
			}
			c.FirstKeptEntryID = to
			remapped = c
		case branchSummaryType:
			var b BranchSummaryContent
			if err := json.Unmarshal(part.Data, &b); err != nil {
				return "", err
			}
			to, ok := ids[b.FromID]
			if !ok {
				continue
			}
			b.FromID = to
			remapped = b
		default:
			continue
		}
		out, err := json.Marshal(partWrapper{Type: part.Type, Data: remapped})
		if err != nil {
			return "", fmt.Errorf("encode remapped part: %w", err)
		}
		wrapped[i], changed = out, true
	}
	if !changed {
		return raw, nil
	}
	out, err := json.Marshal(wrapped)
	if err != nil {
		return "", fmt.Errorf("encode remapped parts: %w", err)
	}
	return string(out), nil
}

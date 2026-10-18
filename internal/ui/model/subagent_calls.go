package model

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
)

// subagentCalls is the feature that tracks which task call owns each
// child session's live activity. A child starts owned by the call that
// created it, whose ID is the child's own; each resume message it receives
// hands it to that resume's call. Ownership comes from the event stream
// itself, so events are attributed to the call that produced them however
// late they're read. It never claims a message.
type subagentCalls struct {
	children map[string]*childCalls
	now      func() time.Time
}

// childCalls is what the tracker knows about one child session.
type childCalls struct {
	// owner is the call that owns the child's activity, "" for the call
	// that created it.
	owner string
	// startedAt and baseCost are when owner took over and the child's
	// cost then, so its item shows only what it spent.
	startedAt time.Time
	baseCost  float64
	// lastCost is the child's cost as last seen.
	lastCost float64
}

func newSubagentCalls() *subagentCalls {
	return &subagentCalls{children: make(map[string]*childCalls), now: time.Now}
}

// subagentCallStats is what one call's item shows from a child session
// update.
type subagentCallStats struct {
	callID    string
	cost      float64
	startedAt time.Time
}

func (c *subagentCalls) child(id string) *childCalls {
	ch, ok := c.children[id]
	if !ok {
		ch = &childCalls{}
		c.children[id] = ch
	}
	return ch
}

// Observe implements feature. It follows resume messages, from creation
// or update events in case the creation was dropped, and child costs.
func (c *subagentCalls) Observe(msg tea.Msg) tea.Cmd {
	switch ev := msg.(type) {
	case pubsub.Event[message.Message]:
		callID := resumeCallID(ev.Payload)
		if ev.Type == pubsub.DeletedEvent || callID == "" {
			return nil
		}
		ch := c.child(ev.Payload.SessionID)
		if ch.owner != callID {
			ch.owner, ch.startedAt, ch.baseCost = callID, c.now(), ch.lastCost
		}
	case pubsub.Event[session.Session]:
		if ev.Payload.ParentSessionID != "" {
			c.child(ev.Payload.ID).lastCost = ev.Payload.Cost
		}
	}
	return nil
}

// OwnerCall returns the task call ID (messageID$$toolCallID) whose item
// shows childSessionID's activity. Like the other queries, it's safe on a
// nil tracker, which knows of no resumes.
func (c *subagentCalls) OwnerCall(childSessionID string) string {
	if c == nil {
		return childSessionID
	}
	if ch, ok := c.children[childSessionID]; ok && ch.owner != "" {
		return ch.owner
	}
	return childSessionID
}

// CallStats returns the owning call's view of a child session update. The
// call that created the child counts from the session's start; a resume
// counts from when it took over.
func (c *subagentCalls) CallStats(s session.Session) subagentCallStats {
	if c == nil {
		return subagentCallStats{callID: s.ID, cost: s.Cost}
	}
	if ch, ok := c.children[s.ID]; ok && ch.owner != "" {
		return subagentCallStats{callID: ch.owner, cost: s.Cost - ch.baseCost, startedAt: ch.startedAt}
	}
	var startedAt time.Time
	if s.CreatedAt > 0 {
		startedAt = time.Unix(s.CreatedAt, 0)
	}
	return subagentCallStats{callID: s.ID, cost: s.Cost, startedAt: startedAt}
}

// SeenCost records a child's cost as loaded from the database.
func (c *subagentCalls) SeenCost(childSessionID string, cost float64) {
	if c == nil {
		return
	}
	c.child(childSessionID).lastCost = cost
}

func (c *subagentCalls) HandleMsg(tea.Msg) (tea.Cmd, bool)          { return nil, false }
func (c *subagentCalls) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool)  { return nil, false }
func (c *subagentCalls) HandleAction(dialog.Action) (tea.Cmd, bool) { return nil, false }

// agentItemToolCallID returns the tool call ID of the task item that shows
// childSessionID's live activity.
func (m *UI) agentItemToolCallID(childSessionID string) (string, bool) {
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(m.features.calls.OwnerCall(childSessionID))
	return toolCallID, ok
}

// messagesOfCall returns the messages of child session msgs that the task
// call callID produced. The call that created the child (firstCallID) owns
// everything up to the first resume; each resume owns everything from its
// own injected message up to the next resume.
func messagesOfCall(msgs []message.Message, firstCallID, callID string) []message.Message {
	var out []message.Message
	current := firstCallID
	for _, msg := range msgs {
		if resumed := resumeCallID(msg); resumed != "" {
			current = resumed
		}
		if current == callID {
			out = append(out, msg)
		}
	}
	return out
}

// resumeCallID returns the call a message's resume injection started, or
// "" when it isn't one.
func resumeCallID(msg message.Message) string {
	if msg.MessageType != message.MessageTypeSubagentInjection {
		return ""
	}
	for _, inj := range msg.Injections() {
		if inj.Kind == message.InjectionKindResume {
			return inj.CallID
		}
	}
	return ""
}

// loadedStats is what a reloaded task item shows.
type loadedStats struct {
	cost                  float64
	startedAt, finishedAt time.Time
}

// loadedCallStats picks a reloaded item's stats: the call's own when it
// has a stored link, otherwise the child session's, as before links were
// stored. finishedAt is zero while the call or session hasn't ended.
func loadedCallStats(sess session.Session, call session.SubagentCall, linked bool) loadedStats {
	if linked {
		stats := loadedStats{cost: call.Cost, startedAt: time.Unix(call.CreatedAt, 0)}
		if call.FinishedAt > 0 {
			stats.finishedAt = time.Unix(call.FinishedAt, 0)
		}
		return stats
	}
	var stats loadedStats
	stats.cost = sess.Cost
	if sess.CreatedAt > 0 {
		stats.startedAt = time.Unix(sess.CreatedAt, 0)
	}
	if sess.UpdatedAt > sess.CreatedAt {
		stats.finishedAt = time.Unix(sess.UpdatedAt, 0)
	}
	return stats
}

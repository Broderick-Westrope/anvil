package model

import (
	"time"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

// subagentCalls tracks which task call owns each child session's live
// activity. A child starts owned by the call that created it, whose ID is
// the child's own; each resume message it receives hands it to that
// resume's call. Ownership comes from the event stream itself, so events
// are attributed to the call that produced them however late they're read.
type subagentCalls struct {
	owner map[string]string
	// started is when each resumed call took over its child, and
	// baseCost the child's cost at that moment, so its item shows only
	// what it spent.
	started  map[string]time.Time
	baseCost map[string]float64
	// lastCost is each child's cost as last seen.
	lastCost map[string]float64
}

// subagentCallStats is what one call's item shows from a child session
// update.
type subagentCallStats struct {
	callID    string
	cost      float64
	startedAt time.Time
}

// observe records the call a child's resume message starts.
func (c *subagentCalls) observe(msg message.Message) { c.observeAt(msg, time.Now()) }

func (c *subagentCalls) observeAt(msg message.Message, now time.Time) {
	callID := resumeCallID(msg)
	if callID == "" || c.owner[msg.SessionID] == callID {
		return
	}
	if c.owner == nil {
		c.owner = make(map[string]string)
		c.started = make(map[string]time.Time)
		c.baseCost = make(map[string]float64)
	}
	c.owner[msg.SessionID] = callID
	c.started[callID] = now
	c.baseCost[callID] = c.lastCost[msg.SessionID]
}

// sessionUpdated returns the stats of the call that owns child s. The
// call that created the child counts from the session's start; a resume
// counts from when it took over.
func (c *subagentCalls) sessionUpdated(s session.Session) subagentCallStats {
	if c.lastCost == nil {
		c.lastCost = make(map[string]float64)
	}
	c.lastCost[s.ID] = s.Cost
	callID := c.current(s.ID)
	if started, ok := c.started[callID]; ok {
		return subagentCallStats{callID: callID, cost: s.Cost - c.baseCost[callID], startedAt: started}
	}
	var startedAt time.Time
	if s.CreatedAt > 0 {
		startedAt = time.Unix(s.CreatedAt, 0)
	}
	return subagentCallStats{callID: callID, cost: s.Cost, startedAt: startedAt}
}

// current returns the call that owns childSessionID's activity.
func (c *subagentCalls) current(childSessionID string) string {
	if callID, ok := c.owner[childSessionID]; ok {
		return callID
	}
	return childSessionID
}

// agentItemToolCallID returns the tool call ID of the task item that shows
// childSessionID's live activity.
func (m *UI) agentItemToolCallID(childSessionID string) (string, bool) {
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(m.subagentCalls.current(childSessionID))
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

// seenCost records childSessionID's cost as loaded, so a resume observed
// later counts only what it adds.
func (c *subagentCalls) seenCost(childSessionID string, cost float64) {
	if c.lastCost == nil {
		c.lastCost = make(map[string]float64)
	}
	c.lastCost[childSessionID] = cost
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

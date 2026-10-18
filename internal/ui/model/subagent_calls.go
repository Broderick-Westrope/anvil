package model

import (
	"github.com/Broderick-Westrope/anvil/internal/message"
)

// subagentCalls tracks which task call owns each child session's live
// activity. A child starts owned by the call that created it, whose ID is
// the child's own; each resume message it receives hands it to that
// resume's call. Ownership comes from the event stream itself, so events
// are attributed to the call that produced them however late they're read.
type subagentCalls struct {
	owner map[string]string
}

// observe records the call a child's resume message starts.
func (c *subagentCalls) observe(msg message.Message) {
	callID := resumeCallID(msg)
	if callID == "" {
		return
	}
	if c.owner == nil {
		c.owner = make(map[string]string)
	}
	c.owner[msg.SessionID] = callID
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

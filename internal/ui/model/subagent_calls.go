package model

import (
	"cmp"

	"github.com/Broderick-Westrope/anvil/internal/message"
)

// agentItemToolCallID returns the tool call ID of the task item that shows
// childSessionID's live activity: the call holding the child's lease, which
// is a resume's own item, or else the call that created the child.
func (m *UI) agentItemToolCallID(childSessionID string) (string, bool) {
	callID := cmp.Or(m.com.Workspace.AgentSubagentActiveCall(childSessionID), childSessionID)
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(callID)
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

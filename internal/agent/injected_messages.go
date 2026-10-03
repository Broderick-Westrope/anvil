package agent

import "charm.land/fantasy"

// injectedMessages records messages Anvil adds to a run between steps (such
// as user prompts queued while the agent was busy).
//
// Fantasy rebuilds each step's input from the initial prompt plus the
// responses generated so far, so anything a PrepareStep callback appends is
// only visible to the step it was added in. Without re-applying them, the
// model sees a queued prompt for one step and then it silently disappears
// from every later request in the same run.
type injectedMessages struct {
	// baseLen is the length of fantasy's initial prompt, captured on the
	// first step. Messages beyond it are generated responses.
	baseLen int
	entries []injectedMessageEntry
}

type injectedMessageEntry struct {
	// responseOffset is the number of generated response messages that
	// preceded the injection.
	responseOffset int
	messages       []fantasy.Message
}

func newInjectedMessages() *injectedMessages {
	return &injectedMessages{baseLen: -1}
}

// apply returns a fresh slice containing stepMessages with every previously
// injected message re-inserted at the position it was originally added.
// stepMessages is never modified.
func (im *injectedMessages) apply(stepMessages []fantasy.Message) []fantasy.Message {
	if im.baseLen < 0 {
		im.baseLen = len(stepMessages)
	}

	total := len(stepMessages)
	for _, entry := range im.entries {
		total += len(entry.messages)
	}
	result := make([]fantasy.Message, 0, total)

	baseLen := min(im.baseLen, len(stepMessages))
	result = append(result, stepMessages[:baseLen]...)
	responses := stepMessages[baseLen:]

	consumed := 0
	for _, entry := range im.entries {
		offset := min(entry.responseOffset, len(responses))
		if offset > consumed {
			result = append(result, responses[consumed:offset]...)
			consumed = offset
		}
		result = append(result, entry.messages...)
	}
	result = append(result, responses[consumed:]...)
	return result
}

// add records messages injected after the responses contained in
// stepMessages, so later steps can re-apply them at the same position.
// stepMessages must be the unmodified step input passed to apply.
func (im *injectedMessages) add(stepMessages []fantasy.Message, messages ...fantasy.Message) {
	if len(messages) == 0 {
		return
	}
	if im.baseLen < 0 {
		im.baseLen = len(stepMessages)
	}
	im.entries = append(im.entries, injectedMessageEntry{
		responseOffset: max(len(stepMessages)-im.baseLen, 0),
		messages:       messages,
	})
}

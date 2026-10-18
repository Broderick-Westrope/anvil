package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestMessagesOfCallSplitsAChildAtEachResume(t *testing.T) {
	t.Parallel()

	resume := func(id, callID string) message.Message {
		return message.Message{
			ID: id, Role: message.User, MessageType: message.MessageTypeSubagentInjection,
			Parts: message.InjectionParts([]message.InjectionContent{{
				Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, CallID: callID,
			}}),
		}
	}
	steer := func(id, callID string) message.Message {
		return message.Message{
			ID: id, Role: message.User, MessageType: message.MessageTypeSubagentInjection,
			Parts: message.InjectionParts([]message.InjectionContent{{
				Source: message.InjectionSourceHuman, Kind: message.InjectionKindSteer, CallID: callID,
			}}),
		}
	}
	msgs := []message.Message{
		{ID: "prompt", Role: message.User},
		{ID: "a1", Role: message.Assistant},
		steer("s1", "m$$c1"),
		{ID: "a2", Role: message.Assistant},
		resume("r2", "m2$$c2"),
		{ID: "a3", Role: message.Assistant},
		resume("r3", "m3$$c3"),
		{ID: "a4", Role: message.Assistant},
	}

	ids := func(callID string) []string {
		var out []string
		for _, m := range messagesOfCall(msgs, "m$$c1", callID) {
			out = append(out, m.ID)
		}
		return out
	}
	require.Equal(t, []string{"prompt", "a1", "s1", "a2"}, ids("m$$c1"))
	require.Equal(t, []string{"r2", "a3"}, ids("m2$$c2"))
	require.Equal(t, []string{"r3", "a4"}, ids("m3$$c3"))
}

func TestSubagentCallOwnershipFollowsResumeMessages(t *testing.T) {
	t.Parallel()
	var calls subagentCalls

	require.Equal(t, "m1$$c1", calls.current("m1$$c1"))

	calls.observe(message.Message{
		SessionID: "m1$$c1", Role: message.User, MessageType: message.MessageTypeSubagentInjection,
		Parts: message.InjectionParts([]message.InjectionContent{{
			Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, CallID: "m2$$c2",
		}}),
	})
	require.Equal(t, "m2$$c2", calls.current("m1$$c1"))

	calls.observe(message.Message{SessionID: "m1$$c1", Role: message.Assistant})
	require.Equal(t, "m2$$c2", calls.current("m1$$c1"), "ordinary messages don't change ownership")
	require.Equal(t, "other$$c", calls.current("other$$c"))
}

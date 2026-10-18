package model

import (
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
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

func TestSubagentCallStatsCountOnlyTheirOwnCost(t *testing.T) {
	t.Parallel()
	var calls subagentCalls
	now := time.Unix(2000, 0)

	stats := calls.sessionUpdated(session.Session{ID: "m1$$c1", Cost: 0.25, CreatedAt: 1000})
	require.Equal(t, subagentCallStats{callID: "m1$$c1", cost: 0.25, startedAt: time.Unix(1000, 0)}, stats)

	calls.observeAt(message.Message{
		SessionID: "m1$$c1", Role: message.User, MessageType: message.MessageTypeSubagentInjection,
		Parts: message.InjectionParts([]message.InjectionContent{{
			Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, CallID: "m2$$c2",
		}}),
	}, now)

	stats = calls.sessionUpdated(session.Session{ID: "m1$$c1", Cost: 0.40, CreatedAt: 1000})
	require.Equal(t, "m2$$c2", stats.callID)
	require.InDelta(t, 0.15, stats.cost, 1e-9)
	require.Equal(t, now, stats.startedAt)
}

func TestLoadedCallStatsPreferTheCallsOwn(t *testing.T) {
	t.Parallel()
	sess := session.Session{Cost: 0.40, CreatedAt: 1000, UpdatedAt: 3000}

	got := loadedCallStats(sess, session.SubagentCall{Cost: 0.15, CreatedAt: 2000, FinishedAt: 2500}, true)
	require.Equal(t, loadedStats{cost: 0.15, startedAt: time.Unix(2000, 0), finishedAt: time.Unix(2500, 0)}, got)

	got = loadedCallStats(sess, session.SubagentCall{}, false)
	require.Equal(t, loadedStats{cost: 0.40, startedAt: time.Unix(1000, 0), finishedAt: time.Unix(3000, 0)}, got)
}

func TestSeenCostBaselinesALaterResume(t *testing.T) {
	t.Parallel()
	var calls subagentCalls
	calls.seenCost("m1$$c1", 0.40)
	calls.observeAt(message.Message{
		SessionID: "m1$$c1", Role: message.User, MessageType: message.MessageTypeSubagentInjection,
		Parts: message.InjectionParts([]message.InjectionContent{{
			Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, CallID: "m3$$c3",
		}}),
	}, time.Unix(5000, 0))
	stats := calls.sessionUpdated(session.Session{ID: "m1$$c1", Cost: 0.50})
	require.InDelta(t, 0.10, stats.cost, 1e-9)
}

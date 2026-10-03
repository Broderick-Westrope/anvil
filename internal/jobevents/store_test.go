package jobevents

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

type fakeOwners struct {
	mu     sync.Mutex
	owners map[string]string
}

func (f *fakeOwners) set(jobID, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners[jobID] = sessionID
}

func (f *fakeOwners) forget(jobID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.owners, jobID)
}

func (f *fakeOwners) owner(jobID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.owners[jobID]
	return s, ok
}

func newTestStore(t *testing.T, owners map[string]string) (*Store, *fakeOwners) {
	t.Helper()
	f := &fakeOwners{owners: owners}
	return NewStore(f.owner), f
}

func info(jobID, sessionID string) shell.JobInfo {
	return shell.JobInfo{ID: jobID, SessionID: sessionID, Done: true, ExitCode: 1}
}

func eventIDs(events []Event) []int64 {
	ids := make([]int64, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return ids
}

func jobIDsOf(events []Event) []string {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.JobID)
	}
	return ids
}

func drainSignal(s *Store) bool {
	select {
	case <-s.Pending():
		return true
	default:
		return false
	}
}

func TestStore_ClaimMaxAndRemaining(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a", "002": "a", "003": "a", "004": "b"})
	s.JobCompleted(info("001", "a"), "one")
	s.JobCompleted(info("004", "b"), "")
	s.JobCompleted(info("002", "a"), "")
	s.JobCompleted(info("003", "a"), "")

	claimed, remaining := s.Claim("a", 2)
	require.Equal(t, []string{"001", "002"}, jobIDsOf(claimed))
	require.Equal(t, 1, remaining)
	require.Equal(t, StateClaimed, claimed[0].State)
	require.Equal(t, "one", claimed[0].Tail)

	s.MarkDelivered(eventIDs(claimed))
	claimed, remaining = s.Claim("a", 2)
	require.Equal(t, []string{"003"}, jobIDsOf(claimed))
	require.Zero(t, remaining)
	s.MarkDelivered(eventIDs(claimed))

	claimed, _ = s.Claim("a", 2)
	require.Empty(t, claimed)
	require.True(t, s.HasPending("b"))
	require.False(t, s.HasPending("a"))
}

func TestStore_ReleaseReturnsToPendingWithoutSignal(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a"})
	s.JobCompleted(info("001", "a"), "")
	require.True(t, drainSignal(s))

	claimed, _ := s.Claim("a", 5)
	require.Len(t, claimed, 1)
	require.False(t, s.HasPending("a"))

	s.Release(eventIDs(claimed))
	require.False(t, drainSignal(s))
	require.True(t, s.HasPending("a"))

	again, _ := s.Claim("a", 5)
	require.Equal(t, eventIDs(claimed), eventIDs(again))
}

func TestStore_ObserveBeforeCompletion(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a"})
	s.Observe("001", KindCompleted, 0)
	s.JobCompleted(info("001", "a"), "")

	require.False(t, s.HasPending("a"))
	require.False(t, drainSignal(s))
	s.mu.Lock()
	require.Len(t, s.events, 1)
	require.Equal(t, StateSuperseded, s.events[0].State)
	s.mu.Unlock()
}

func TestStore_ObserveAfterCompletion(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a"})
	s.JobCompleted(info("001", "a"), "")
	require.True(t, s.HasPending("a"))

	s.Observe("001", KindCompleted, 0)
	require.False(t, s.HasPending("a"))
	claimed, _ := s.Claim("a", 5)
	require.Empty(t, claimed)
}

func TestStore_ObserveWhileClaimedSupersedesOnRelease(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a"})
	s.JobCompleted(info("001", "a"), "")
	claimed, _ := s.Claim("a", 5)
	s.Observe("001", KindCompleted, 0)
	s.Release(eventIDs(claimed))
	require.False(t, s.HasPending("a"))
}

func TestStore_StaleWatchGenDropped(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a"})
	s.WatchReplaced("001", 1)
	s.WatchReplaced("001", 2)
	s.PatternMatched(info("001", "a"), 1, "old")
	require.False(t, s.HasPending("a"))

	s.PatternMatched(info("001", "a"), 2, "new")
	claimed, _ := s.Claim("a", 5)
	require.Len(t, claimed, 1)
	require.Equal(t, KindMatched, claimed[0].Kind)
	require.Equal(t, uint64(2), claimed[0].WatchGen)
	require.Equal(t, "new", claimed[0].Line)
}

func TestStore_WatchReplacedSupersedesOlderMatches(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a"})
	s.WatchReplaced("001", 1)
	s.PatternMatched(info("001", "a"), 1, "old")
	require.True(t, s.HasPending("a"))

	s.WatchReplaced("001", 2)
	require.False(t, s.HasPending("a"))
}

func TestStore_ObserveMatch(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a"})
	s.WatchReplaced("001", 3)
	s.Observe("001", KindMatched, 3)
	s.PatternMatched(info("001", "a"), 3, "seen")
	require.False(t, s.HasPending("a"))
}

func TestStore_OwnerChangeMovesClaims(t *testing.T) {
	t.Parallel()

	s, owners := newTestStore(t, map[string]string{"001": "child"})
	s.JobCompleted(info("001", "child"), "")
	require.True(t, s.HasPending("child"))

	owners.set("001", "parent")
	claimed, _ := s.Claim("child", 5)
	require.Empty(t, claimed)
	claimed, _ = s.Claim("parent", 5)
	require.Len(t, claimed, 1)
}

func TestStore_SnapshotFallbackAfterEviction(t *testing.T) {
	t.Parallel()

	s, owners := newTestStore(t, map[string]string{"001": "child"})
	s.JobCompleted(info("001", "child"), "")
	s.Reassign([]string{"001"}, "parent")
	owners.forget("001")

	claimed, _ := s.Claim("child", 5)
	require.Empty(t, claimed)
	claimed, _ = s.Claim("parent", 5)
	require.Len(t, claimed, 1)
	require.Equal(t, "parent", claimed[0].Info.SessionID)
}

func TestStore_DropJobs(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{"001": "a", "002": "a"})
	s.WatchReplaced("001", 1)
	s.PatternMatched(info("001", "a"), 1, "x")
	s.DropJobs([]string{"001"})
	s.JobCompleted(info("001", "a"), "")
	s.JobCompleted(info("002", "a"), "")

	claimed, remaining := s.Claim("a", 5)
	require.Equal(t, []string{"002"}, jobIDsOf(claimed))
	require.Zero(t, remaining)
}

func TestStore_PendingSignalNeverBlocks(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t, map[string]string{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 1000 {
			s.JobCompleted(info(fmt.Sprintf("%03X", i), "a"), "")
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("JobCompleted blocked with nobody reading Pending")
	}
	require.True(t, drainSignal(s))
	require.False(t, drainSignal(s))
}

func TestStore_PrunesSettledEventsAndUnknownJobs(t *testing.T) {
	t.Parallel()

	s, owners := newTestStore(t, map[string]string{"001": "a", "002": "a"})
	now := time.Now()
	s.now = func() time.Time { return now }

	s.JobCompleted(info("001", "a"), "")
	s.Observe("001", KindCompleted, 0)
	s.DropJobs([]string{"002"})
	owners.forget("001")

	now = now.Add(retention + time.Second)
	s.Claim("a", 5)
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Empty(t, s.events)
	require.NotContains(t, s.observed, "001")
	require.NotContains(t, s.touched, "001")
	require.Contains(t, s.dropped, "002", "known jobs keep their bookkeeping")
}

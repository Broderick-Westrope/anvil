package session_test

import (
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

func newSubagentTestService(t *testing.T) session.Service {
	t.Helper()
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Release(dataDir) })
	return session.NewService(db.New(conn), conn)
}

func newChild(t *testing.T, svc session.Service, parentID, id string) session.Session {
	t.Helper()
	child, err := svc.CreateTaskSession(t.Context(), id, parentID, "child")
	require.NoError(t, err)
	return child
}

func TestCreateSubagentAssignsATypedHandle(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)
	child := newChild(t, svc, parent.ID, "msg-1$$call-1")

	created, err := svc.CreateSubagent(t.Context(), session.Subagent{
		SessionID:       child.ID,
		ParentSessionID: parent.ID,
		Origin:          session.SubagentOriginTask,
		AgentType:       "fixer",
		Depth:           2,
		Provider:        "anthropic",
		Model:           "claude",
	})
	require.NoError(t, err)
	require.Regexp(t, `^fixer-[0-9a-f]{4}$`, created.Handle)

	got, err := svc.GetSubagentByHandle(t.Context(), parent.ID, created.Handle)
	require.NoError(t, err)
	require.Equal(t, created, got)

	byID, err := svc.GetSubagent(t.Context(), child.ID)
	require.NoError(t, err)
	require.Equal(t, created, byID)
}

func TestCreateSubagentLengthensACollidingHandle(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)

	// Find two child IDs whose handles share the first four hash digits.
	first, second := collidingChildIDs(t)
	handles := make([]string, 0, 2)
	for _, id := range []string{first, second} {
		child := newChild(t, svc, parent.ID, id)
		created, err := svc.CreateSubagent(t.Context(), session.Subagent{
			SessionID: child.ID, ParentSessionID: parent.ID, Origin: session.SubagentOriginTask,
			AgentType: "explorer", Provider: "p", Model: "m",
		})
		require.NoError(t, err)
		handles = append(handles, created.Handle)
	}
	require.Len(t, handles[0], len("explorer-")+4)
	require.Len(t, handles[1], len("explorer-")+5)
	require.True(t, strings.HasPrefix(handles[1], handles[0]))
}

func collidingChildIDs(t *testing.T) (string, string) {
	t.Helper()
	seen := map[string]string{}
	for i := range 100000 {
		id := "msg$$call-" + strconv.Itoa(i)
		prefix := session.HashID(id)[:4]
		if other, ok := seen[prefix]; ok && session.HashID(other)[:5] != session.HashID(id)[:5] {
			return other, id
		}
		seen[prefix] = id
	}
	t.Fatal("no colliding IDs found")
	return "", ""
}

func TestGetSubagentByHandleOnlyFindsDirectChildren(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)
	other, err := svc.Create(t.Context(), "other", "/tmp")
	require.NoError(t, err)
	child := newChild(t, svc, parent.ID, "msg$$call")
	created, err := svc.CreateSubagent(t.Context(), session.Subagent{
		SessionID: child.ID, ParentSessionID: parent.ID, Origin: session.SubagentOriginTask,
		AgentType: "fixer", Provider: "p", Model: "m",
	})
	require.NoError(t, err)

	_, err = svc.GetSubagentByHandle(t.Context(), other.ID, created.Handle)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestSubagentCallLinksACallToItsChildSession(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)
	child := newChild(t, svc, parent.ID, "msg-1$$call-1")

	call := session.SubagentCall{ID: "msg-2$$call-9", SessionID: child.ID, Kind: session.SubagentCallResume}
	require.NoError(t, svc.CreateSubagentCall(t.Context(), call))

	got, err := svc.GetSubagentCall(t.Context(), call.ID)
	require.NoError(t, err)
	require.Equal(t, call.ID, got.ID)
	require.Equal(t, call.SessionID, got.SessionID)
	require.Equal(t, call.Kind, got.Kind)
}

func TestRollUpChildCostAddsOnlyNewCost(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)
	child := newChild(t, svc, parent.ID, "msg$$call")

	setCost(t, svc, child.ID, 0.25)
	require.NoError(t, svc.RollUpChildCost(t.Context(), child.ID, ""))
	require.NoError(t, svc.RollUpChildCost(t.Context(), child.ID, ""))
	requireCost(t, svc, parent.ID, 0.25)

	setCost(t, svc, child.ID, 0.40)
	require.NoError(t, svc.RollUpChildCost(t.Context(), child.ID, ""))
	requireCost(t, svc, parent.ID, 0.40)
}

func TestRollUpChildCostFromSiblingsAtOnceLosesNothing(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)

	const siblings = 8
	ids := make([]string, siblings)
	for i := range siblings {
		ids[i] = newChild(t, svc, parent.ID, "msg$$call-"+strconv.Itoa(i)).ID
		setCost(t, svc, ids[i], 0.1)
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() { require.NoError(t, svc.RollUpChildCost(t.Context(), id, "")) })
	}
	wg.Wait()
	requireCost(t, svc, parent.ID, 0.8)
}

func setCost(t *testing.T, svc session.Service, id string, cost float64) {
	t.Helper()
	s, err := svc.Get(t.Context(), id)
	require.NoError(t, err)
	s.Cost = cost
	_, err = svc.Save(t.Context(), s)
	require.NoError(t, err)
}

func requireCost(t *testing.T, svc session.Service, id string, want float64) {
	t.Helper()
	s, err := svc.Get(t.Context(), id)
	require.NoError(t, err)
	require.InDelta(t, want, s.Cost, 1e-9)
}

func TestRollUpChildCostFailsWhenTheParentIsGone(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)
	child := newChild(t, svc, parent.ID, "msg$$call")
	setCost(t, svc, child.ID, 0.25)
	require.NoError(t, svc.Delete(t.Context(), parent.ID))

	require.ErrorContains(t, svc.RollUpChildCost(t.Context(), child.ID, ""), "parent session")
}

func TestStaleParentSaveKeepsRolledUpCost(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)
	child := newChild(t, svc, parent.ID, "msg$$call")

	stale, err := svc.Get(t.Context(), parent.ID)
	require.NoError(t, err)
	setCost(t, svc, child.ID, 0.30)
	require.NoError(t, svc.RollUpChildCost(t.Context(), child.ID, ""))

	// A save from a copy read before the rollup, such as a todos update,
	// adds its own change without undoing the rollup.
	stale.Todos = []session.Todo{{Content: "x", Status: session.TodoStatusPending}}
	stale.Cost += 0.05
	_, err = svc.Save(t.Context(), stale)
	require.NoError(t, err)
	requireCost(t, svc, parent.ID, 0.35)
}

func TestRollUpChildCostAlsoChargesTheCall(t *testing.T) {
	t.Parallel()
	svc := newSubagentTestService(t)
	parent, err := svc.Create(t.Context(), "parent", "/tmp")
	require.NoError(t, err)
	child := newChild(t, svc, parent.ID, "m1$$c1")
	require.NoError(t, svc.CreateSubagentCall(t.Context(), session.SubagentCall{ID: "m1$$c1", SessionID: child.ID, Kind: session.SubagentCallNew}))
	require.NoError(t, svc.CreateSubagentCall(t.Context(), session.SubagentCall{ID: "m2$$c2", SessionID: child.ID, Kind: session.SubagentCallResume}))

	setCost(t, svc, child.ID, 0.25)
	require.NoError(t, svc.RollUpChildCost(t.Context(), child.ID, "m1$$c1"))
	setCost(t, svc, child.ID, 0.40)
	require.NoError(t, svc.RollUpChildCost(t.Context(), child.ID, "m2$$c2"))
	require.NoError(t, svc.FinishSubagentCall(t.Context(), "m2$$c2"))

	first, err := svc.GetSubagentCall(t.Context(), "m1$$c1")
	require.NoError(t, err)
	second, err := svc.GetSubagentCall(t.Context(), "m2$$c2")
	require.NoError(t, err)
	require.InDelta(t, 0.25, first.Cost, 1e-9)
	require.InDelta(t, 0.15, second.Cost, 1e-9)
	require.Zero(t, first.FinishedAt)
	require.NotZero(t, second.FinishedAt)
	require.NotZero(t, second.CreatedAt)
	requireCost(t, svc, parent.ID, 0.40)
}

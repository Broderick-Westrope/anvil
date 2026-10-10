package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

type idleTestWorkspace struct {
	testWorkspace
}

func (w *idleTestWorkspace) AgentIsReady() bool { return false }

func TestNewTodosLeavePillsCollapsed(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.com.Workspace = &idleTestWorkspace{}
	u.height = 60
	u.session = &session.Session{ID: "s1"}
	u.updateLayoutAndSize()

	u.update(pubsub.Event[session.Session]{
		Type: pubsub.UpdatedEvent,
		Payload: session.Session{ID: "s1", Todos: []session.Todo{
			{Status: session.TodoStatusInProgress, Content: "do work"},
			{Status: session.TodoStatusPending, Content: "do more"},
		}},
	})

	require.False(t, u.pillsExpanded)
	require.Equal(t, pillHeightWithBorder, u.pillsAreaHeight())
}

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

func newTodosToolForTest(t *testing.T, existing []session.Todo) (fantasy.AgentTool, session.Service, context.Context) {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	sessions := session.NewService(db.New(conn), conn)
	sess, err := sessions.Create(t.Context(), "Todos", t.TempDir())
	require.NoError(t, err)
	if existing != nil {
		sess.Todos = existing
		_, err = sessions.Save(t.Context(), sess)
		require.NoError(t, err)
	}
	ctx := context.WithValue(t.Context(), SessionIDContextKey, sess.ID)
	return NewTodosTool(sessions), sessions, ctx
}

func runTodosTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, todos ...TodoItem) (fantasy.ToolResponse, error) {
	t.Helper()
	input, err := json.Marshal(TodosParams{Todos: todos})
	require.NoError(t, err)
	return tool.Run(ctx, fantasy.ToolCall{ID: "test-call", Name: TodosToolName, Input: string(input)})
}

func todosMetadata(t *testing.T, resp fantasy.ToolResponse) TodosResponseMetadata {
	t.Helper()
	var meta TodosResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	return meta
}

func TestTodosTool_RequiresSession(t *testing.T) {
	t.Parallel()
	tool, _, _ := newTodosToolForTest(t, nil)

	_, err := runTodosTool(t, tool, t.Context())

	require.EqualError(t, err, "session ID is required for managing todos")
}

func TestTodosTool_UnknownSessionIsAnError(t *testing.T) {
	t.Parallel()
	tool, _, _ := newTodosToolForTest(t, nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "missing")

	_, err := runTodosTool(t, tool, ctx)

	require.ErrorContains(t, err, "failed to get session: ")
}

func TestTodosTool_RejectsInvalidStatusWithoutSaving(t *testing.T) {
	t.Parallel()
	existing := []session.Todo{{Content: "keep", Status: session.TodoStatusPending}}
	tool, sessions, ctx := newTodosToolForTest(t, existing)

	_, err := runTodosTool(t, tool, ctx,
		TodoItem{Content: "fine", Status: "pending"},
		TodoItem{Content: "broken", Status: "done"},
	)

	require.EqualError(t, err, `invalid status "done" for todo "broken"`)
	sess, err := sessions.Get(t.Context(), GetSessionFromContext(ctx))
	require.NoError(t, err)
	require.Equal(t, existing, sess.Todos)
}

func TestTodosTool_SavesTodosAndReportsCounts(t *testing.T) {
	t.Parallel()
	tool, sessions, ctx := newTodosToolForTest(t, nil)

	resp, err := runTodosTool(t, tool, ctx,
		TodoItem{Content: "a", Status: "pending"},
		TodoItem{Content: "b", Status: "pending", ActiveForm: "Doing b"},
		TodoItem{Content: "c", Status: "in_progress", ActiveForm: "Doing c"},
		TodoItem{Content: "d", Status: "completed"},
		TodoItem{Content: "e", Status: "completed"},
		TodoItem{Content: "f", Status: "completed"},
	)

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "Todo list updated successfully.\n\n"+
		"Status: 2 pending, 1 in progress, 3 completed\n"+
		"Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress. Please proceed with the current tasks if applicable.", resp.Content)
	want := []session.Todo{
		{Content: "a", Status: session.TodoStatusPending},
		{Content: "b", Status: session.TodoStatusPending, ActiveForm: "Doing b"},
		{Content: "c", Status: session.TodoStatusInProgress, ActiveForm: "Doing c"},
		{Content: "d", Status: session.TodoStatusCompleted},
		{Content: "e", Status: session.TodoStatusCompleted},
		{Content: "f", Status: session.TodoStatusCompleted},
	}
	require.Equal(t, TodosResponseMetadata{
		IsNew:       true,
		Todos:       want,
		JustStarted: "Doing c",
		Completed:   3,
		Total:       6,
	}, todosMetadata(t, resp))
	sess, err := sessions.Get(t.Context(), GetSessionFromContext(ctx))
	require.NoError(t, err)
	require.Equal(t, want, sess.Todos)
}

func TestTodosTool_ReportsTransitions(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		existing          []session.Todo
		update            []TodoItem
		wantJustCompleted []string
		wantJustStarted   string
	}{
		"pending to completed is just completed": {
			existing:          []session.Todo{{Content: "a", Status: session.TodoStatusPending}},
			update:            []TodoItem{{Content: "a", Status: "completed"}},
			wantJustCompleted: []string{"a"},
		},
		"in progress to completed is just completed": {
			existing:          []session.Todo{{Content: "a", Status: session.TodoStatusInProgress}},
			update:            []TodoItem{{Content: "a", Status: "completed"}},
			wantJustCompleted: []string{"a"},
		},
		"already completed is not just completed": {
			existing: []session.Todo{{Content: "a", Status: session.TodoStatusCompleted}},
			update:   []TodoItem{{Content: "a", Status: "completed"}},
		},
		"new completed todo is not just completed": {
			existing: []session.Todo{{Content: "other", Status: session.TodoStatusPending}},
			update:   []TodoItem{{Content: "a", Status: "completed"}},
		},
		"several just completed keep their order": {
			existing: []session.Todo{
				{Content: "a", Status: session.TodoStatusPending},
				{Content: "b", Status: session.TodoStatusPending},
			},
			update:            []TodoItem{{Content: "b", Status: "completed"}, {Content: "a", Status: "completed"}},
			wantJustCompleted: []string{"b", "a"},
		},
		"pending to in progress is just started with its active form": {
			existing:        []session.Todo{{Content: "a", Status: session.TodoStatusPending}},
			update:          []TodoItem{{Content: "a", Status: "in_progress", ActiveForm: "Doing a"}},
			wantJustStarted: "Doing a",
		},
		"just started falls back to content without an active form": {
			existing:        []session.Todo{{Content: "a", Status: session.TodoStatusPending}},
			update:          []TodoItem{{Content: "a", Status: "in_progress"}},
			wantJustStarted: "a",
		},
		"new in progress todo is just started": {
			existing:        []session.Todo{{Content: "other", Status: session.TodoStatusPending}},
			update:          []TodoItem{{Content: "a", Status: "in_progress", ActiveForm: "Doing a"}},
			wantJustStarted: "Doing a",
		},
		"completed to in progress is just started": {
			existing:        []session.Todo{{Content: "a", Status: session.TodoStatusCompleted}},
			update:          []TodoItem{{Content: "a", Status: "in_progress"}},
			wantJustStarted: "a",
		},
		"still in progress is not just started": {
			existing: []session.Todo{{Content: "a", Status: session.TodoStatusInProgress, ActiveForm: "Doing a"}},
			update:   []TodoItem{{Content: "a", Status: "in_progress", ActiveForm: "Doing a"}},
		},
		"last started todo wins": {
			existing:        []session.Todo{{Content: "other", Status: session.TodoStatusPending}},
			update:          []TodoItem{{Content: "a", Status: "in_progress"}, {Content: "b", Status: "in_progress"}},
			wantJustStarted: "b",
		},
		"pending update is neither": {
			existing: []session.Todo{{Content: "a", Status: session.TodoStatusCompleted}},
			update:   []TodoItem{{Content: "a", Status: "pending"}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tool, _, ctx := newTodosToolForTest(t, tc.existing)

			resp, err := runTodosTool(t, tool, ctx, tc.update...)

			require.NoError(t, err)
			meta := todosMetadata(t, resp)
			require.False(t, meta.IsNew)
			require.Equal(t, tc.wantJustCompleted, meta.JustCompleted)
			require.Equal(t, tc.wantJustStarted, meta.JustStarted)
		})
	}
}

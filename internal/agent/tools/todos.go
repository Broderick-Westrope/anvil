package tools

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

//go:embed todos.md
var todosDescription string

const TodosToolName = "todos"

type TodosParams struct {
	Todos []TodoItem `json:"todos" description:"The updated todo list"`
}

type TodoItem struct {
	Content    string `json:"content" description:"What needs to be done (imperative form)"`
	Status     string `json:"status" description:"Task status: pending, in_progress, or completed"`
	ActiveForm string `json:"active_form" description:"Present continuous form (e.g., 'Running tests')"`
}

type TodosResponseMetadata struct {
	IsNew         bool           `json:"is_new"`
	Todos         []session.Todo `json:"todos"`
	JustCompleted []string       `json:"just_completed,omitempty"`
	JustStarted   string         `json:"just_started,omitempty"`
	Completed     int            `json:"completed"`
	Total         int            `json:"total"`
}

func NewTodosTool(sessions session.Service) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		TodosToolName,
		todosDescription,
		func(ctx context.Context, params TodosParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for managing todos")
			}

			currentSession, err := sessions.Get(ctx, sessionID)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to get session: %w", err)
			}

			isNew := len(currentSession.Todos) == 0
			oldStatusByContent := make(map[string]session.TodoStatus)
			for _, todo := range currentSession.Todos {
				oldStatusByContent[todo.Content] = todo.Status
			}

			// countByStatus counts the new todos by status. Its keys are the
			// statuses a todo may have.
			countByStatus := map[session.TodoStatus]int{
				session.TodoStatusPending:    0,
				session.TodoStatusInProgress: 0,
				session.TodoStatusCompleted:  0,
			}
			todos := make([]session.Todo, len(params.Todos))
			var justCompleted []string
			var justStarted string

			for i, item := range params.Todos {
				newStatus := session.TodoStatus(item.Status)
				if _, ok := countByStatus[newStatus]; !ok {
					return fantasy.ToolResponse{}, fmt.Errorf("invalid status %q for todo %q", item.Status, item.Content)
				}
				countByStatus[newStatus]++
				todos[i] = session.Todo{
					Content:    item.Content,
					Status:     newStatus,
					ActiveForm: item.ActiveForm,
				}

				oldStatus, existed := oldStatusByContent[item.Content]
				if newStatus == session.TodoStatusCompleted && existed && oldStatus != session.TodoStatusCompleted {
					justCompleted = append(justCompleted, item.Content)
				}
				if newStatus == session.TodoStatusInProgress && (!existed || oldStatus != session.TodoStatusInProgress) {
					justStarted = cmp.Or(item.ActiveForm, item.Content)
				}
			}

			currentSession.Todos = todos
			_, err = sessions.Save(ctx, currentSession)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("failed to save todos: %w", err)
			}

			response := "Todo list updated successfully.\n\n"

			pendingCount := countByStatus[session.TodoStatusPending]
			inProgressCount := countByStatus[session.TodoStatusInProgress]
			completedCount := countByStatus[session.TodoStatusCompleted]

			response += fmt.Sprintf("Status: %d pending, %d in progress, %d completed\n",
				pendingCount, inProgressCount, completedCount)

			response += "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress. Please proceed with the current tasks if applicable."

			metadata := TodosResponseMetadata{
				IsNew:         isNew,
				Todos:         todos,
				JustCompleted: justCompleted,
				JustStarted:   justStarted,
				Completed:     completedCount,
				Total:         len(todos),
			}

			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
		})
}

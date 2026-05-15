package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/google/uuid"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/pubsub"
)

//go:embed templates/background_task_tool.md
var backgroundTaskToolDescription string

// BackgroundTaskParams holds the parameters for the background_task tool.
type BackgroundTaskParams struct {
	Prompt       string `json:"prompt" jsonschema:"description=The task for the agent to perform,required"`
	SubagentType string `json:"subagent_type" jsonschema:"description=The type of specialized agent to use,required"`
	Description  string `json:"description" jsonschema:"description=A short (3-5 words) description of the task"`
}

const (
	BackgroundTaskToolName = "background_task"

	// BackgroundTaskCompletedEvent is published when a background task completes successfully.
	BackgroundTaskCompletedEvent pubsub.EventType = "background_task_completed"

	// BackgroundTaskFailedEvent is published when a background task fails.
	BackgroundTaskFailedEvent pubsub.EventType = "background_task_failed"
)

// backgroundTaskTool builds the background_task delegation tool for the given
// caller agent. callerName is the agent's own ID; callerDepth is its current
// delegation depth. The tool fires a goroutine and returns immediately with a
// task_id; the result is published to bgBroker on completion.
func (c *coordinator) backgroundTaskTool(ctx context.Context, callerName string, callerDepth int) (fantasy.AgentTool, error) {
	return fantasy.NewAgentTool(
		BackgroundTaskToolName,
		backgroundTaskToolDescription,
		func(ctx context.Context, params BackgroundTaskParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}
			if params.SubagentType == "" {
				return fantasy.NewTextErrorResponse("subagent_type is required"), nil
			}

			// Validate that the requested agent type is configured.
			if _, ok := c.agentConfigs[params.SubagentType]; !ok {
				validTypes := make([]string, 0, len(c.agentConfigs))
				for name := range c.agentConfigs {
					if name != config.AgentOrchestrator {
						validTypes = append(validTypes, name)
					}
				}
				slices.Sort(validTypes)
				return fantasy.NewTextErrorResponse(
					fmt.Sprintf("unknown subagent_type %q; valid types: %s",
						params.SubagentType, strings.Join(validTypes, ", ")),
				), nil
			}

			// Enforce delegation rules: non-orchestrator callers may only delegate
			// to agents listed in their delegates_to frontmatter.
			if callerName != config.AgentOrchestrator {
				callerMD, hasMD := c.agentMDs[callerName]
				if hasMD && !slices.Contains(callerMD.DelegatesTo, params.SubagentType) {
					return fantasy.NewTextErrorResponse(
						fmt.Sprintf("agent %q is not allowed to delegate to %q; allowed: %s",
							callerName, params.SubagentType,
							strings.Join(callerMD.DelegatesTo, ", ")),
					), nil
				}
			}

			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			// Generate a unique ID for this background task.
			taskID := uuid.New().String()

			// Try to acquire the semaphore non-blocking to cap concurrency at 10.
			select {
			case c.bgSemaphore <- struct{}{}:
			default:
				return fantasy.NewTextErrorResponse(
					"background task concurrency limit reached (max 10); wait for existing tasks to complete before launching new ones",
				), nil
			}

			// Use context.Background() so the goroutine survives past the tool
			// call return. Explicit cancellation is provided via the cancel func
			// stored in bgTasks; the caller can cancel individual tasks or the
			// session teardown path cancels them all via CancelBackgroundTask.
			taskCtx, cancel := context.WithCancel(context.Background())
			c.bgTasks.Set(taskID, cancel)

			description := params.Description
			if description == "" {
				description = "Background task"
			}

			// Snapshot values needed in the goroutine before returning.
			targetDepth := callerDepth - 1
			subagentType := params.SubagentType
			prompt := params.Prompt

			go func() {
				defer func() {
					// Release semaphore slot and remove from active-tasks map.
					<-c.bgSemaphore
					c.bgTasks.Del(taskID)
					cancel()
				}()

				// Build (or reuse) the target agent.
				agent, err := c.getOrBuildAgent(taskCtx, subagentType, targetDepth)
				if err != nil {
					slog.Error("Background task: failed to build agent",
						"task_id", taskID,
						"agent", subagentType,
						"error", err,
					)
					result := BackgroundTaskResult{
						TaskID:    taskID,
						AgentName: subagentType,
						Result:    fmt.Sprintf("Failed to build agent: %s", err),
						Success:   false,
					}
					c.bgBroker.Publish(BackgroundTaskFailedEvent, result)
					return
				}

				// Truncate the prompt for logging.
				promptSummary := prompt
				if len(promptSummary) > 100 {
					promptSummary = promptSummary[:100]
				}
				slog.Info("Background task started",
					"task_id", taskID,
					"agent", subagentType,
					"depth", targetDepth,
					"description", description,
					"task_summary", promptSummary,
				)

				// Run the sub-agent; cost rollup to parent session is handled
				// inside runSubAgent.
				resp, err := c.runSubAgent(taskCtx, subAgentParams{
					Agent:          agent,
					SessionID:      sessionID,
					AgentMessageID: agentMessageID,
					ToolCallID:     taskID,
					Prompt:         prompt,
					SessionTitle:   description,
				})
				if err != nil {
					slog.Error("Background task failed",
						"task_id", taskID,
						"agent", subagentType,
						"error", err,
					)
					result := BackgroundTaskResult{
						TaskID:    taskID,
						AgentName: subagentType,
						Result:    fmt.Sprintf("Task failed: %s", err),
						Success:   false,
					}
					c.bgBroker.Publish(BackgroundTaskFailedEvent, result)
					return
				}

				slog.Info("Background task completed",
					"task_id", taskID,
					"agent", subagentType,
				)
				result := BackgroundTaskResult{
					TaskID:    taskID,
					AgentName: subagentType,
					Result:    resp.Content,
					Success:   true,
				}
				c.bgBroker.Publish(BackgroundTaskCompletedEvent, result)
			}()

			return fantasy.NewTextResponse(fmt.Sprintf("Background task started with ID: %s", taskID)), nil
		}), nil
}

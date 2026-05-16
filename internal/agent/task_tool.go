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

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
)

//go:embed templates/task_tool.md
var taskToolDescription string

// TaskParams holds the parameters for the task tool.
type TaskParams struct {
	Prompt       string `json:"prompt" jsonschema:"description=The task for the agent to perform,required"`
	SubagentType string `json:"subagent_type" jsonschema:"description=The type of specialized agent to use for this task,required"`
	Description  string `json:"description" jsonschema:"description=A short (3-5 words) description of the task"`
	Model        string `json:"model" jsonschema:"description=Optional model override (provider/model format e.g. anthropic/claude-opus-4-6). When set the agent uses this model instead of its configured default."`
}

const (
	TaskToolName = "task"
)

// taskTool builds the task delegation tool for the given caller agent.
// callerName is the agent's own ID (e.g. "orchestrator"); callerDepth is its
// current delegation depth (3 = top-level orchestrator).
func (c *coordinator) taskTool(ctx context.Context, callerName string, callerDepth int) (fantasy.AgentTool, error) {
	return fantasy.NewParallelAgentTool(
		TaskToolName,
		taskToolDescription,
		func(ctx context.Context, params TaskParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
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

			// Lazily build (or reuse) the target agent at depth-1.
			targetDepth := callerDepth - 1
			agent, err := c.getOrBuildAgent(ctx, params.SubagentType, targetDepth, params.Model)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("building agent %q: %w", params.SubagentType, err)
			}

			// Truncate the prompt for logging.
			promptSummary := params.Prompt
			if len(promptSummary) > 100 {
				promptSummary = promptSummary[:100]
			}
			slog.Info("Delegating to agent",
				"agent", params.SubagentType,
				"depth", targetDepth,
				"model_override", params.Model,
				"task_summary", promptSummary,
			)

			return c.runSubAgent(ctx, subAgentParams{
				Agent:          agent,
				SessionID:      sessionID,
				AgentMessageID: agentMessageID,
				ToolCallID:     call.ID,
				Prompt:         params.Prompt,
				SessionTitle:   params.Description,
			})
		}), nil
}

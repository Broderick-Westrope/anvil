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
	Model        string `json:"model,omitempty" jsonschema:"description=Rarely needed. Overrides the agent's configured model (exact provider/model ID). Omit unless you have a specific reason to deviate from the agent's tuned default."`
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

			if refusal := c.delegationRefusal(callerName, params.SubagentType); refusal != "" {
				return fantasy.NewTextErrorResponse(refusal), nil
			}
			params.Model = c.usableModelOverride(params.SubagentType, params.Model)

			sessionID, agentMessageID, err := subAgentOrigin(ctx)
			if err != nil {
				return fantasy.ToolResponse{}, err
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

// delegationRefusal returns why callerName may not delegate to target, or
// "" when it may. The target must be a configured agent, and callers other
// than the orchestrator may only delegate to the agents listed in their
// delegates_to frontmatter.
func (c *coordinator) delegationRefusal(callerName, target string) string {
	// Snapshot what's needed under orchestratorMu, which ReloadPlugins
	// holds while swapping these maps.
	c.orchestratorMu.RLock()
	_, configured := c.agentConfigs[target]
	var validTypes []string
	if !configured {
		validTypes = make([]string, 0, len(c.agentConfigs))
		for name := range c.agentConfigs {
			if name != config.AgentOrchestrator {
				validTypes = append(validTypes, name)
			}
		}
	}
	callerMD, hasMD := c.agentMDs[callerName]
	c.orchestratorMu.RUnlock()

	if !configured {
		slices.Sort(validTypes)
		return fmt.Sprintf("unknown subagent_type %q; valid types: %s", target, strings.Join(validTypes, ", "))
	}
	if callerName != config.AgentOrchestrator && hasMD && !slices.Contains(callerMD.DelegatesTo, target) {
		return fmt.Sprintf("agent %q is not allowed to delegate to %q; allowed: %s",
			callerName, target, strings.Join(callerMD.DelegatesTo, ", "))
	}
	return ""
}

// usableModelOverride returns the model override for agent, or "" when it
// doesn't resolve. An unresolvable override (typically a hallucinated
// model ID) would otherwise fall through to the global large model,
// silently promoting a cheap specialist onto the most expensive model
// available. Dropping it instead keeps the agent on its configured
// default.
func (c *coordinator) usableModelOverride(agent, model string) string {
	if model == "" {
		return ""
	}
	candidate := config.Agent{ID: agent, Model: model}
	if _, err := config.ResolveAgentModel(candidate, c.cfg.Config()); err != nil {
		slog.Warn("Ignoring invalid model override; using the agent's configured model",
			"agent", agent,
			"model_override", model,
			"error", err,
		)
		return ""
	}
	return model
}

// subAgentOrigin returns the session and assistant message a sub-agent
// tool call came from. The sub-agent's child session hangs off them.
func subAgentOrigin(ctx context.Context) (sessionID, agentMessageID string, err error) {
	sessionID = tools.GetSessionFromContext(ctx)
	if sessionID == "" {
		return "", "", errors.New("session id missing from context")
	}
	agentMessageID = tools.GetMessageFromContext(ctx)
	if agentMessageID == "" {
		return "", "", errors.New("agent message id missing from context")
	}
	return sessionID, agentMessageID, nil
}

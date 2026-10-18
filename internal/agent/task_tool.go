package agent

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"charm.land/fantasy"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

//go:embed templates/task_tool.md
var taskToolDescription string

// TaskParams holds the parameters for the task tool.
type TaskParams struct {
	Prompt       string `json:"prompt" jsonschema:"description=The task for the agent to perform. When resuming, the follow-up message for it,required"`
	SubagentType string `json:"subagent_type,omitempty" jsonschema:"description=The type of specialized agent to use for this task. Required unless resuming"`
	Description  string `json:"description" jsonschema:"description=A short (3-5 words) description of the task"`
	Model        string `json:"model,omitempty" jsonschema:"description=Rarely needed. Overrides the agent's configured model (exact provider/model ID). Omit unless you have a specific reason to deviate from the agent's tuned default."`
	Resume       string `json:"resume,omitempty" jsonschema:"description=The subagent_id of a subagent you spawned earlier in this session. Continues it with its history instead of starting a new one"`
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
			if params.Resume != "" {
				return c.resumeSubagent(ctx, callerName, callerDepth, params, call)
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
				AgentType:      params.SubagentType,
				Depth:          targetDepth,
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
func subAgentOrigin(ctx context.Context) (string, string, error) {
	sessionID := tools.GetSessionFromContext(ctx)
	if sessionID == "" {
		return "", "", errors.New("session id missing from context")
	}
	agentMessageID := tools.GetMessageFromContext(ctx)
	if agentMessageID == "" {
		return "", "", errors.New("agent message id missing from context")
	}
	return sessionID, agentMessageID, nil
}

// resumeSubagent continues a direct child subagent of the calling session
// with params.Prompt, on the agent type and model it was stored with.
func (c *coordinator) resumeSubagent(ctx context.Context, callerName string, callerDepth int, params TaskParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID, agentMessageID, err := subAgentOrigin(ctx)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	sub, refusal := c.resumableSubagent(ctx, sessionID, params)
	if refusal != "" {
		return fantasy.NewTextErrorResponse(refusal), nil
	}
	override, refusal := c.storedModelOverride(sub)
	if refusal != "" {
		return fantasy.NewTextErrorResponse(refusal), nil
	}
	if refusal := c.delegationRefusal(callerName, sub.AgentType); refusal != "" {
		return fantasy.NewTextErrorResponse(refusal), nil
	}
	agent, err := c.getOrBuildAgent(ctx, sub.AgentType, callerDepth-1, override)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("building agent %q: %w", sub.AgentType, err)
	}
	slog.Info("Resuming subagent", "subagent_id", sub.Handle, "agent", sub.AgentType, "depth", callerDepth-1)
	return c.runSubAgent(ctx, subAgentParams{
		Agent:          agent,
		AgentType:      sub.AgentType,
		Depth:          sub.Depth,
		SessionID:      sessionID,
		AgentMessageID: agentMessageID,
		ToolCallID:     call.ID,
		Prompt:         params.Prompt,
		SessionTitle:   params.Description,
		Resume:         &sub,
	})
}

// resumableSubagent looks up the subagent params.Resume names among the
// direct children of sessionID, or says why it can't be resumed.
func (c *coordinator) resumableSubagent(ctx context.Context, sessionID string, params TaskParams) (session.Subagent, string) {
	sub, err := c.sessions.GetSubagentByHandle(ctx, sessionID, params.Resume)
	if errors.Is(err, sql.ErrNoRows) {
		if child, getErr := c.sessions.Get(ctx, params.Resume); getErr == nil && child.ParentSessionID == sessionID {
			return session.Subagent{}, fmt.Sprintf("subagent %q was created before subagents could be resumed; start a new one", params.Resume)
		}
		return session.Subagent{}, fmt.Sprintf("unknown subagent_id %q: no subagent this session spawned has it", params.Resume)
	}
	if err != nil {
		return session.Subagent{}, fmt.Sprintf("look up subagent_id %q: %s", params.Resume, err)
	}
	if params.SubagentType != "" && params.SubagentType != sub.AgentType {
		return session.Subagent{}, fmt.Sprintf("subagent_id %q is a %q subagent, not %q; omit subagent_type to resume it", sub.Handle, sub.AgentType, params.SubagentType)
	}
	if stored := sub.Provider + "/" + sub.Model; params.Model != "" && params.Model != stored {
		return session.Subagent{}, fmt.Sprintf("subagent_id %q runs on %q; omit model to resume it", sub.Handle, stored)
	}
	return sub, ""
}

// storedModelOverride returns the model override that rebuilds sub's agent
// on the model it was stored with ("" when that's the agent's default), or
// says why it can't.
func (c *coordinator) storedModelOverride(sub session.Subagent) (string, string) {
	c.orchestratorMu.RLock()
	agentCfg, ok := c.agentConfigs[sub.AgentType]
	c.orchestratorMu.RUnlock()
	if !ok {
		return "", fmt.Sprintf("subagent_id %q can't be resumed: its agent %q is no longer configured", sub.Handle, sub.AgentType)
	}
	stored := sub.Provider + "/" + sub.Model
	resolved, err := config.ResolveAgentModel(config.Agent{ID: sub.AgentType, Model: stored}, c.cfg.Config())
	if _, providerOK := c.cfg.Config().Providers.Get(sub.Provider); err != nil || !providerOK || resolved.Provider != sub.Provider || resolved.Model != sub.Model {
		return "", fmt.Sprintf("subagent_id %q can't be resumed: its model %q is no longer available", sub.Handle, stored)
	}
	if def, err := config.ResolveAgentModel(agentCfg, c.cfg.Config()); err == nil && def.Provider == sub.Provider && def.Model == sub.Model {
		return "", ""
	}
	return stored, ""
}

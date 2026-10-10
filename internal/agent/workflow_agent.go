package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"charm.land/fantasy"
	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// SubmitResultToolName is the tool a workflow subagent calls to return
// structured output.
const SubmitResultToolName = "submit_result"

// workflowAgentDepth matches a specialist delegated to by the orchestrator.
const workflowAgentDepth = 2

// WorkflowAgentCall runs a fresh, isolated subagent on behalf of a
// workflow.
type WorkflowAgentCall struct {
	ParentSessionID string
	Agent           string
	Prompt          string
	Title           string
	// Tools, when non-nil, is the exact tool set (built-in and MCP). An
	// unknown name or one the agent can't use is an error, never a
	// fallback to the full set.
	Tools []string
	// Model pins a provider/model. An unresolvable model is an error.
	Model string
	// Schema, when set, gives the agent a submit_result tool with this
	// input schema. Validate checks each submission.
	Schema   json.RawMessage
	Validate func(json.RawMessage) error
}

// WorkflowAgentOutput is what a workflow subagent produced. Output is nil
// when a schema was given but the agent never submitted a valid result.
type WorkflowAgentOutput struct {
	SessionID string
	Text      string
	Output    json.RawMessage
}

// RunWorkflowAgent implements Coordinator.
func (c *coordinator) RunWorkflowAgent(ctx context.Context, call WorkflowAgentCall) (WorkflowAgentOutput, error) {
	c.orchestratorMu.RLock()
	agentCfg, ok := c.agentConfigs[call.Agent]
	c.orchestratorMu.RUnlock()
	if !ok || call.Agent == config.AgentOrchestrator {
		return WorkflowAgentOutput{}, fmt.Errorf("agent %q not configured", call.Agent)
	}
	if call.Model != "" {
		agentCfg.Model = call.Model
		if _, err := config.ResolveAgentModel(agentCfg, c.cfg.Config()); err != nil {
			return WorkflowAgentOutput{}, fmt.Errorf("model %q for agent %q: %w", call.Model, call.Agent, err)
		}
	}

	// Restrictions and schemas build their own instance rather than using
	// the shared agent cache, so they can't leak between calls.
	var sub SessionAgent
	var submit *submitResultTool
	var err error
	if call.Tools == nil && call.Schema == nil && call.Model == "" {
		sub, err = c.getOrBuildAgent(ctx, call.Agent, workflowAgentDepth, "")
	} else {
		sub, submit, err = c.buildWorkflowAgent(ctx, call, agentCfg)
	}
	if err != nil {
		return WorkflowAgentOutput{}, err
	}

	title := call.Title
	if title == "" {
		title = "workflow: " + call.Agent
	}
	ctx = context.WithValue(ctx, ownerKey{}, struct{}{})
	session, err := c.sessions.CreateTaskSession(ctx, uuid.NewString(), call.ParentSessionID, title)
	if err != nil {
		return WorkflowAgentOutput{}, fmt.Errorf("create session: %w", err)
	}
	defer func() {
		if inventory := handOffSubagentJobs(shell.GetBackgroundShellManager(), c.jobEvents, session.ID, call.ParentSessionID); inventory != "" {
			slog.Info("Workflow subagent jobs handed off", "child_session", session.ID, "inventory", inventory)
		}
	}()
	defer c.permissions.RevokeAutoApproveSession(session.ID)

	text, err := c.runWorkflowTurn(ctx, sub, session.ID, call.Prompt)
	if err != nil {
		return WorkflowAgentOutput{}, err
	}
	if submit != nil && submit.result() == nil {
		// One nudge, in the same session, before giving up.
		text, err = c.runWorkflowTurn(ctx, sub, session.ID,
			"You ended without calling "+SubmitResultToolName+". Call it now with your result.")
		if err != nil {
			return WorkflowAgentOutput{}, err
		}
	}
	if err := c.updateParentSessionCost(ctx, session.ID, call.ParentSessionID); err != nil {
		slog.Warn("Failed to update parent session cost", "child_session", session.ID, "error", err)
	}
	out := WorkflowAgentOutput{SessionID: session.ID, Text: text}
	if submit != nil {
		out.Output = submit.result()
	}
	return out, nil
}

func (c *coordinator) runWorkflowTurn(ctx context.Context, sub SessionAgent, sessionID, prompt string) (string, error) {
	call, err := c.subAgentCall(ctx, sub, sessionID, prompt)
	if err != nil {
		return "", err
	}
	result, err := sub.Run(ctx, call)
	if err != nil {
		return "", err
	}
	return subAgentOutput(result), nil
}

// buildWorkflowAgent builds an uncached agent whose tools are restricted to
// call.Tools (when set) plus submit_result (when a schema is set).
func (c *coordinator) buildWorkflowAgent(ctx context.Context, call WorkflowAgentCall, agentCfg config.Agent) (SessionAgent, *submitResultTool, error) {
	available, _, err := c.buildTools(ctx, agentCfg, workflowAgentDepth)
	if err != nil {
		return nil, nil, err
	}
	agentTools := available
	if call.Tools != nil {
		byName := make(map[string]fantasy.AgentTool, len(available))
		for _, t := range available {
			byName[t.Info().Name] = t
		}
		agentTools = make([]fantasy.AgentTool, 0, len(call.Tools))
		var unknown []string
		for _, name := range call.Tools {
			t, ok := byName[name]
			if !ok {
				unknown = append(unknown, name)
				continue
			}
			agentTools = append(agentTools, t)
		}
		if len(unknown) > 0 {
			return nil, nil, fmt.Errorf("agent %q can't use tools: %s", call.Agent, strings.Join(unknown, ", "))
		}
	}
	var submit *submitResultTool
	if call.Schema != nil {
		submit, err = newSubmitResultTool(call.Schema, call.Validate)
		if err != nil {
			return nil, nil, err
		}
		agentTools = append(slices.Clone(agentTools), submit)
	}

	sub, err := c.buildAgent(ctx, call.Agent, agentCfg, workflowAgentDepth, true)
	if err != nil {
		return nil, nil, err
	}
	sub.SetTools(agentTools)
	sub.SetLazyMCPToolMap(nil)
	return sub, submit, nil
}

// submitResultTool takes a workflow subagent's structured result. Its
// input schema is the workflow's schema; invalid submissions go back to
// the model as tool errors so it can retry within the run.
type submitResultTool struct {
	info     fantasy.ToolInfo
	validate func(json.RawMessage) error
	// wrapped is set when the schema isn't an object: tool inputs must be
	// objects, so the value travels under a "result" key.
	wrapped bool

	mu        sync.Mutex
	submitted json.RawMessage
	opts      fantasy.ProviderOptions
}

func newSubmitResultTool(schema json.RawMessage, validate func(json.RawMessage) error) (*submitResultTool, error) {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	t := &submitResultTool{validate: validate}
	if s["type"] != "object" {
		t.wrapped = true
		s = map[string]any{"type": "object", "properties": map[string]any{"result": s}, "required": []any{"result"}}
	}
	props, _ := s["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	var required []string
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				required = append(required, name)
			}
		}
	}
	t.info = fantasy.ToolInfo{
		Name:        SubmitResultToolName,
		Description: "Submit your final result. Call this exactly once, when you're done, with input matching the schema.",
		Parameters:  props,
		Required:    required,
	}
	return t, nil
}

func (t *submitResultTool) Info() fantasy.ToolInfo { return t.info }

func (t *submitResultTool) Run(_ context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	doc := json.RawMessage(call.Input)
	if t.wrapped {
		var in struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(doc, &in); err != nil {
			return fantasy.NewTextErrorResponse("Invalid result: " + err.Error()), nil
		}
		doc = in.Result
	}
	if t.validate != nil {
		if err := t.validate(doc); err != nil {
			return fantasy.NewTextErrorResponse("Invalid result: " + err.Error() + ". Fix it and call " + SubmitResultToolName + " again."), nil
		}
	}
	t.mu.Lock()
	t.submitted = slices.Clone(doc)
	t.mu.Unlock()
	return fantasy.NewTextResponse("Result recorded. You're done; end your turn."), nil
}

func (t *submitResultTool) result() json.RawMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.submitted
}

func (t *submitResultTool) ProviderOptions() fantasy.ProviderOptions {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opts
}

func (t *submitResultTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.opts = opts
}

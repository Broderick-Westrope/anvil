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

	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
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
	// SessionID, when set, is the ID of the child session, so a caller can
	// point at it before the agent finishes. Otherwise one is generated.
	SessionID string
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
	// OnSession, when set, is called with the child session's ID once the
	// session exists, before the agent runs in it.
	OnSession func(sessionID string)
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

	// A workflow's restrictions and schemas build their own instance rather
	// than using the shared agent cache, so they can't leak between calls.
	// A pinned model is part of the cache key, so it can share. (Branch
	// summaries cache their restricted instance under a key that includes
	// the tool set; see summarizerCacheKey.)
	var sub SessionAgent
	var submit *submitResultTool
	var err error
	if call.Tools == nil && call.Schema == nil {
		sub, err = c.getOrBuildAgent(ctx, call.Agent, workflowAgentDepth, call.Model)
	} else {
		sub, submit, err = c.buildWorkflowAgent(ctx, call, agentCfg)
	}
	if err != nil {
		return WorkflowAgentOutput{}, err
	}
	return c.runWorkflowAgent(ctx, call, sub, submit)
}

// runWorkflowAgent runs sub, built for call, in a new child session.
func (c *coordinator) runWorkflowAgent(ctx context.Context, call WorkflowAgentCall, sub SessionAgent, submit *submitResultTool) (WorkflowAgentOutput, error) {
	title := call.Title
	if title == "" {
		title = "workflow: " + call.Agent
	}
	sessionID := call.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	out := WorkflowAgentOutput{SessionID: sessionID}
	handedOff, err := c.inChildSession(ctx, childSession{
		ID: sessionID, ParentID: call.ParentSessionID, Title: title, OnCreate: call.OnSession,
	}, func(ctx context.Context, sessionID string) error {
		text, err := c.runWorkflowTurn(ctx, sub, sessionID, call.Prompt)
		if err != nil {
			return err
		}
		if submit != nil && submit.result() == nil {
			// One nudge, in the same session, before giving up.
			text, err = c.runWorkflowTurn(ctx, sub, sessionID,
				"You ended without calling "+SubmitResultToolName+". Call it now with your result.")
			if err != nil {
				return err
			}
		}
		out.Text = text
		return nil
	})
	if handedOff != "" {
		slog.Info("Workflow subagent jobs handed off", "parent_session", call.ParentSessionID, "inventory", handedOff)
	}
	if err != nil {
		return WorkflowAgentOutput{}, err
	}
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
	call.Await = true
	result, err := sub.Run(ctx, call)
	if err != nil {
		return "", err
	}
	return subAgentOutput(result), nil
}

// buildWorkflowAgent builds an agent whose tools are restricted to
// call.Tools (when set) plus submit_result (when a schema is set). It
// doesn't cache it; a caller that does must key the cache on the
// restriction.
func (c *coordinator) buildWorkflowAgent(ctx context.Context, call WorkflowAgentCall, agentCfg config.Agent) (SessionAgent, *submitResultTool, error) {
	// MCP servers register their tools during initialisation; listing tools
	// before it finishes would reject valid MCP tool names and snapshot an
	// incomplete set.
	if err := toolsmcp.WaitForInit(ctx); err != nil {
		return nil, nil, fmt.Errorf("wait for MCP initialization: %w", err)
	}
	available, listing, err := c.buildTools(ctx, agentCfg, workflowAgentDepth, call.Tools...)
	if err != nil {
		return nil, nil, err
	}
	// Lazy MCP servers don't connect at startup, so a tool the workflow
	// names from one isn't registered yet: connect its server first.
	if servers := lazyServersFor(missingTools(call.Tools, available), c.allowedLazyMCPs(agentCfg)); servers != nil {
		for _, server := range servers {
			if err := toolsmcp.ConnectDeferred(ctx, server, c.cfg); err != nil {
				return nil, nil, fmt.Errorf("connect MCP server %q: %w", server, err)
			}
		}
		if available, listing, err = c.buildTools(ctx, agentCfg, workflowAgentDepth, call.Tools...); err != nil {
			return nil, nil, err
		}
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
	// Lazy MCP tools stay hidden until enabled, as for any agent, except
	// ones the workflow asked for by name.
	for _, name := range call.Tools {
		delete(listing.lazyTools, name)
	}
	sub.SetMCPListing(listing)
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
		// Can't fail: fantasy only runs a tool once its input parsed as a
		// JSON object.
		_ = json.Unmarshal(doc, &in)
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

// allowedLazyMCPs lists the lazy MCP servers agentCfg may use, by name.
func (c *coordinator) allowedLazyMCPs(agentCfg config.Agent) map[string]string {
	access := toolAccess{allowedMCP: agentCfg.AllowedMCP}
	lazy := make(map[string]string)
	for name, mcpCfg := range c.cfg.Config().MCP {
		if mcpCfg.IsLazy() && access.allowsMCPServer(name) {
			lazy[name] = mcpCfg.LazyDescription
		}
	}
	return lazy
}

func missingTools(names []string, available []fantasy.AgentTool) []string {
	var missing []string
	for _, name := range names {
		if !slices.ContainsFunc(available, func(t fantasy.AgentTool) bool { return t.Info().Name == name }) {
			missing = append(missing, name)
		}
	}
	return missing
}

// lazyServersFor returns the lazy servers whose tools the names refer to.
// MCP tool names are mcp_<server>_<tool>, with the server capitalised
// under OAuth renaming, so the match ignores case.
func lazyServersFor(names []string, lazy map[string]string) []string {
	var servers []string
	for _, name := range names {
		for server := range lazy {
			if strings.HasPrefix(strings.ToLower(name), "mcp_"+strings.ToLower(server)+"_") && !slices.Contains(servers, server) {
				servers = append(servers, server)
			}
		}
	}
	return servers
}

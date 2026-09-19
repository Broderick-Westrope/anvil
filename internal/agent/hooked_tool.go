package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/hooks"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/tidwall/sjson"
)

// hookedTool wraps a fantasy.AgentTool to run PreToolUse hooks before
// delegating to the inner tool.
type hookedTool struct {
	inner  fantasy.AgentTool
	runner *hooks.Runner
}

func newHookedTool(inner fantasy.AgentTool, runner *hooks.Runner) *hookedTool {
	return &hookedTool{inner: inner, runner: runner}
}

// wrapToolsWithHooks returns a tool slice with each entry wrapped in a
// hookedTool. Returns the original slice unchanged when runner is nil or
// when isSubAgent is true — sub-agents never fire hooks, the top-level
// invocation of the sub-agent tool itself is wrapped on the caller's side.
func wrapToolsWithHooks(tools []fantasy.AgentTool, runner *hooks.Runner, isSubAgent bool) []fantasy.AgentTool {
	if runner == nil || isSubAgent {
		return tools
	}
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		out[i] = newHookedTool(tool, runner)
	}
	return out
}

func (h *hookedTool) Info() fantasy.ToolInfo {
	return h.inner.Info()
}

func (h *hookedTool) ProviderOptions() fantasy.ProviderOptions {
	return h.inner.ProviderOptions()
}

func (h *hookedTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	h.inner.SetProviderOptions(opts)
}

func (h *hookedTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	originalCtx := ctx
	resolver, hasResolver := h.inner.(tools.HookTargetResolver)
	if !hasResolver {
		return h.runSinglePass(ctx, call, call.Input)
	}

	preparedInput, preparedCtx, err := resolver.PrepareHookInput(ctx, call.Input)
	if err != nil {
		slog.Debug("Preparing hook input for canonical-target gating failed; proceeding without it",
			"tool", call.Name, "error", err)
		preparedInput = call.Input
	}
	ctx = preparedCtx
	baseline, _ := tools.GetSkillLoadBaseline(ctx)

	sessionID := tools.GetSessionFromContext(ctx)
	result, hookErr := h.runner.Run(ctx, hooks.EventPreToolUse, sessionID, call.Name, preparedInput)
	if hookErr != nil {
		slog.Warn("Hook execution error, proceeding with tool call",
			"tool", call.Name, "error", hookErr)
	}
	if result.HookCount == 0 {
		return h.inner.Run(originalCtx, call)
	}
	if result.Decision == hooks.DecisionDeny || result.Halt {
		return h.blockedResponse(result), nil
	}

	mergedInput := preparedInput
	if result.UpdatedInput != "" {
		mergedInput = result.UpdatedInput
	}

	if baseline.Mode != "name" {
		call.Input = mergedInput
		return h.finishSinglePass(ctx, call, result)
	}

	t0 := tools.HookTarget(baseline)

	t1, err := resolver.CanonicalTarget(ctx, mergedInput)
	if err != nil {
		return boundedTargetErrorResponse(err, result)
	}

	if t1.Mode == "none" || (t1.Mode == "name" && !t1.Resolved) {
		call.Input = mergedInput
		resp, runErr := h.inner.Run(ctx, call)
		if runErr != nil {
			return resp, runErr
		}
		return h.finishResponse(resp, result), nil
	}

	if targetsEqual(t0, t1) {
		call.Input = mergedInput
		return h.finishSinglePass(ctx, call, result)
	}

	finalInput, err := sjson.Set(mergedInput, "file_path", t1.Location)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("normalizing retargeted skill load input: %w", err)
	}

	if t1.Mode == "path" {
		finalInput, err = sjson.Set(finalInput, "skill_name", "")
		if err != nil {
			return fantasy.ToolResponse{}, fmt.Errorf("normalizing retargeted path input: %w", err)
		}
	}
	ctx = tools.WithSkillLoadBaseline(ctx, tools.SkillLoadBaseline{
		Mode: "name", Name: t1.Name, Location: t1.Location, Resolved: true,
	})
	result2, hookErr2 := h.runner.Run(ctx, hooks.EventPreToolUse, sessionID, call.Name, finalInput)
	if hookErr2 != nil {
		slog.Warn("Hook execution error on retarget gate, proceeding with tool call",
			"tool", call.Name, "error", hookErr2)
	}
	combined := mergeRetargetResults(result, result2)
	if result2.Decision == hooks.DecisionDeny || result2.Halt {
		return h.finishResponse(h.blockedResponse(combined), combined), nil
	}

	mergedInput2 := finalInput
	if result2.UpdatedInput != "" {
		mergedInput2 = result2.UpdatedInput
	}

	t2, err := resolver.CanonicalTarget(ctx, mergedInput2)
	if err != nil || !targetsEqual(t1, t2) {
		resp := fantasy.NewTextErrorResponse(
			"PreToolUse hooks retargeted this call twice; a rewritten target may be rewritten once")
		return h.finishResponse(resp, combined), nil
	}

	call.Input = mergedInput2
	if result2.Decision == hooks.DecisionAllow {
		ctx = permission.WithHookApproval(ctx, call.ID)
	}
	resp, err := h.inner.Run(ctx, call)
	if err != nil {
		return resp, err
	}
	return h.finishResponse(resp, combined), nil
}

func (h *hookedTool) runSinglePass(ctx context.Context, call fantasy.ToolCall, input string) (fantasy.ToolResponse, error) {
	sessionID := tools.GetSessionFromContext(ctx)
	result, err := h.runner.Run(ctx, hooks.EventPreToolUse, sessionID, call.Name, input)
	if err != nil {
		slog.Warn("Hook execution error, proceeding with tool call",
			"tool", call.Name, "error", err)
	}
	if result.Decision == hooks.DecisionDeny || result.Halt {
		return h.blockedResponse(result), nil
	}
	if result.UpdatedInput != "" {
		call.Input = result.UpdatedInput
	} else {
		call.Input = input
	}
	return h.finishSinglePass(ctx, call, result)
}

func (h *hookedTool) finishSinglePass(ctx context.Context, call fantasy.ToolCall, result hooks.AggregateResult) (fantasy.ToolResponse, error) {
	// An explicit allow from a hook pre-approves the permission prompt for
	// this tool call. Deny is already handled above; silence falls through
	// to the normal permission flow.
	if result.Decision == hooks.DecisionAllow {
		ctx = permission.WithHookApproval(ctx, call.ID)
	}
	resp, err := h.inner.Run(ctx, call)
	if err != nil {
		return resp, err
	}
	return h.finishResponse(resp, result), nil
}

func (h *hookedTool) blockedResponse(result hooks.AggregateResult) fantasy.ToolResponse {
	reason := fmt.Sprintf("Tool call blocked by hook. Reason: %s", result.Reason)
	if result.Halt {
		reason = fmt.Sprintf("Turn halted by hook. Reason: %s", result.Reason)
	}
	resp := fantasy.NewTextErrorResponse(reason)
	// Halt ends the whole turn; a plain deny only blocks this tool
	// call so the model can see the error and try something else.
	resp.StopTurn = result.Halt
	resp.Metadata = hookMetadataJSON(result)
	return resp
}

func (h *hookedTool) finishResponse(resp fantasy.ToolResponse, result hooks.AggregateResult) fantasy.ToolResponse {
	if result.Context != "" {
		if resp.Content != "" {
			resp.Content += "\n"
		}
		resp.Content += result.Context
	}
	resp.Metadata = mergeHookMetadata(resp.Metadata, result)
	return resp
}

func targetsEqual(a, b tools.HookTarget) bool {
	return a.Resolved && b.Resolved && a.Location == b.Location
}

func boundedTargetErrorResponse(err error, result hooks.AggregateResult) (fantasy.ToolResponse, error) {
	if errors.Is(err, tools.ErrAmbiguousRewrite) || errors.Is(err, tools.ErrPathToNameRewrite) {
		resp := fantasy.NewTextErrorResponse(err.Error())
		resp.Metadata = hookMetadataJSON(result)
		return resp, nil
	}
	return fantasy.ToolResponse{}, err
}

func mergeRetargetResults(pass1, pass2 hooks.AggregateResult) hooks.AggregateResult {
	merged := pass2
	merged.HookCount = pass1.HookCount + pass2.HookCount
	merged.Hooks = append(append([]hooks.HookInfo{}, pass1.Hooks...), pass2.Hooks...)
	merged.Reason = joinNonEmpty(pass1.Reason, pass2.Reason)
	merged.Context = joinNonEmpty(pass1.Context, pass2.Context)
	if merged.UpdatedInput == "" {
		merged.UpdatedInput = pass1.UpdatedInput
	}
	merged.Retarget = true
	return merged
}

func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
}

// buildHookMetadata creates a HookMetadata from an AggregateResult.
func buildHookMetadata(result hooks.AggregateResult) hooks.HookMetadata {
	return hooks.HookMetadata{
		HookCount:    result.HookCount,
		Decision:     result.Decision.String(),
		Halt:         result.Halt,
		Reason:       result.Reason,
		InputRewrite: result.UpdatedInput != "",
		Hooks:        result.Hooks,
		Retarget:     result.Retarget,
	}
}

// hookMetadataJSON builds a JSON string containing only the hook metadata.
func hookMetadataJSON(result hooks.AggregateResult) string {
	meta := buildHookMetadata(result)
	data, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return `{"hook":` + string(data) + `}`
}

// mergeHookMetadata injects hook metadata into existing tool metadata.
func mergeHookMetadata(existing string, result hooks.AggregateResult) string {
	if result.HookCount == 0 && !result.Retarget {
		return existing
	}
	meta := buildHookMetadata(result)
	data, err := json.Marshal(meta)
	if err != nil {
		return existing
	}
	if existing == "" {
		existing = "{}"
	}
	merged, err := sjson.SetRaw(existing, "hook", string(data))
	if err != nil {
		return existing
	}
	return merged
}

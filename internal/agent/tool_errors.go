package agent

import (
	"context"
	"log/slog"

	"charm.land/fantasy"
)

type errorResultTool struct {
	fantasy.AgentTool
}

func wrapToolsWithErrorResults(agentTools []fantasy.AgentTool) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, len(agentTools))
	for i, tool := range agentTools {
		out[i] = errorResultTool{AgentTool: tool}
	}
	return out
}

func (t errorResultTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	resp, err := t.AgentTool.Run(ctx, call)
	if err == nil || ctx.Err() != nil {
		return resp, err
	}
	slog.Warn("Tool failed; reporting the error to the model", "tool", call.Name, "error", err)
	errResp := fantasy.NewTextErrorResponse(err.Error())
	errResp.Metadata = resp.Metadata
	return errResp, nil
}

package agent

import (
	"context"
	"fmt"
	"io/fs"
	"sync/atomic"
	"syscall"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

type stubTool struct {
	fantasy.AgentTool
	run func(ctx context.Context) (fantasy.ToolResponse, error)
}

func (s stubTool) Run(ctx context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return s.run(ctx)
}

func newStubTool(run func(ctx context.Context) (fantasy.ToolResponse, error)) stubTool {
	inner := fantasy.NewAgentTool("stub", "stub tool",
		func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.ToolResponse{}, nil
		})
	return stubTool{AgentTool: inner, run: run}
}

func TestErrorResultTool(t *testing.T) {
	t.Parallel()

	call := fantasy.ToolCall{ID: "tc", Name: "stub", Input: "{}"}

	t.Run("a Go error becomes an error result", func(t *testing.T) {
		t.Parallel()
		tool := errorResultTool{newStubTool(func(context.Context) (fantasy.ToolResponse, error) {
			return fantasy.ToolResponse{Metadata: `{"path":"x"}`}, fmt.Errorf("error accessing file: %w",
				&fs.PathError{Op: "stat", Path: "x", Err: syscall.EACCES})
		})}

		resp, err := tool.Run(t.Context(), call)

		require.NoError(t, err)
		require.True(t, resp.IsError)
		require.Equal(t, "error accessing file: stat x: permission denied", resp.Content)
		require.Equal(t, `{"path":"x"}`, resp.Metadata)
		require.False(t, resp.StopTurn)
	})

	t.Run("a successful response passes through", func(t *testing.T) {
		t.Parallel()
		want := fantasy.NewTextResponse("ok")
		tool := errorResultTool{newStubTool(func(context.Context) (fantasy.ToolResponse, error) {
			return want, nil
		})}

		resp, err := tool.Run(t.Context(), call)

		require.NoError(t, err)
		require.Equal(t, want, resp)
	})

	t.Run("an error response passes through with StopTurn intact", func(t *testing.T) {
		t.Parallel()
		want := fantasy.NewTextErrorResponse("Permission denied")
		want.StopTurn = true
		tool := errorResultTool{newStubTool(func(context.Context) (fantasy.ToolResponse, error) {
			return want, nil
		})}

		resp, err := tool.Run(t.Context(), call)

		require.NoError(t, err)
		require.Equal(t, want, resp)
	})

	t.Run("cancellation still ends the run", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tool := errorResultTool{newStubTool(func(ctx context.Context) (fantasy.ToolResponse, error) {
			return fantasy.ToolResponse{}, ctx.Err()
		})}

		_, err := tool.Run(ctx, call)

		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("a tool's own timeout is reported, not propagated", func(t *testing.T) {
		t.Parallel()
		tool := errorResultTool{newStubTool(func(context.Context) (fantasy.ToolResponse, error) {
			return fantasy.ToolResponse{}, fmt.Errorf("fetch: %w", context.DeadlineExceeded)
		})}

		resp, err := tool.Run(t.Context(), call)

		require.NoError(t, err)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "deadline exceeded")
	})

	t.Run("info and provider options are forwarded", func(t *testing.T) {
		t.Parallel()
		inner := newStubTool(nil)
		tool := errorResultTool{inner}

		require.Equal(t, inner.Info(), tool.Info())
		opts := fantasy.ProviderOptions{"anthropic": nil}
		tool.SetProviderOptions(opts)
		require.Equal(t, opts, inner.ProviderOptions())
	})
}

func TestToolGoErrorContinuesRun(t *testing.T) {
	t.Parallel()

	var runs atomic.Int32
	tool := fantasy.NewAgentTool("touch", "touches a file",
		func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			runs.Add(1)
			return fantasy.ToolResponse{}, fmt.Errorf("touch: %w",
				&fs.PathError{Op: "open", Path: "/root/secret", Err: syscall.EACCES})
		})

	model := &scriptedModel{toolName: "touch", toolSteps: 1}
	agent := fantasy.NewAgent(model, fantasy.WithTools(wrapToolsWithErrorResults([]fantasy.AgentTool{tool})...))

	result, err := agent.Stream(t.Context(), fantasy.AgentStreamCall{Prompt: "go"})

	require.NoError(t, err)
	require.Equal(t, int32(1), runs.Load())
	require.Len(t, model.prompts, 2, "the model must get a second step to react to the error")
	require.Equal(t, "done", result.Response.Content.Text())

	var toolResult *fantasy.ToolResultPart
	for _, msg := range model.prompts[1] {
		for _, part := range msg.Content {
			if tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part); ok {
				toolResult = &tr
			}
		}
	}
	require.NotNil(t, toolResult, "the second step must carry the tool result")
	errOut, ok := toolResult.Output.(fantasy.ToolResultOutputContentError)
	require.True(t, ok, "the result must be an error result, got %T", toolResult.Output)
	require.Contains(t, errOut.Error.Error(), "permission denied")
}

func TestBuildToolsReportsToolErrorsToTheModel(t *testing.T) {
	t.Parallel()
	cfg, err := config.Init(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	cfg.Config().MCP = nil
	c := &coordinator{cfg: cfg}

	for _, depth := range []int{1, 3} {
		builtTools, _, err := c.buildToolsWithState(t.Context(), config.Agent{ID: "all"}, depth, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		require.NotEmpty(t, builtTools)
		for _, tool := range builtTools {
			_, ok := tool.(errorResultTool)
			require.True(t, ok, "depth %d: %s is not wrapped", depth, tool.Info().Name)
		}
	}
}

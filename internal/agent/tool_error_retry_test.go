package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"sync/atomic"
	"syscall"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// A tool's Go error must end the step, not retry it. fantasy below v0.45.1
// classified any wrapped net.Error as a retryable provider failure, and
// syscall.Errno satisfies net.Error, so an ordinary *fs.PathError from a tool
// re-requested the model and re-ran every tool in the step.
func TestToolGoErrorDoesNotRetryStep(t *testing.T) {
	t.Parallel()

	toolErr := &fs.PathError{Op: "open", Path: "/root/secret", Err: syscall.EACCES}
	var netErr net.Error
	require.True(t, errors.As(toolErr, &netErr), "premise: the tool error looks like a net.Error")

	var runs atomic.Int32
	tool := fantasy.NewAgentTool("touch", "touches a file",
		func(context.Context, struct{}, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			runs.Add(1)
			return fantasy.ToolResponse{}, fmt.Errorf("touch: %w", toolErr)
		})

	model := &scriptedModel{toolName: "touch", toolSteps: 1}
	agent := fantasy.NewAgent(model, fantasy.WithTools(tool))

	_, err := agent.Stream(t.Context(), fantasy.AgentStreamCall{Prompt: "go"})

	require.Equal(t, int32(1), runs.Load(), "the tool must run once")
	require.Len(t, model.prompts, 1, "the model must be asked once")
	require.Error(t, err)
	var execErr *fantasy.ToolExecutionError
	require.ErrorAs(t, err, &execErr)
	require.ErrorIs(t, err, syscall.EACCES)
}

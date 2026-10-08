package agent

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func textOf(msg fantasy.Message) string {
	var parts []string
	for _, part := range msg.Content {
		if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "")
}

func TestInjectedMessagesApply(t *testing.T) {
	t.Parallel()

	sys := fantasy.NewSystemMessage("system")
	prompt := fantasy.NewUserMessage("prompt")
	assistant1 := fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "a1"}}}
	tool1 := fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "t1"}}}
	assistant2 := fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "a2"}}}
	tool2 := fantasy.Message{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "t2"}}}
	queued1 := fantasy.NewUserMessage("queued1")
	queued2 := fantasy.NewUserMessage("queued2")

	texts := func(msgs []fantasy.Message) []string {
		out := make([]string, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, textOf(m))
		}
		return out
	}

	im := newInjectedMessages()

	step0 := []fantasy.Message{sys, prompt}
	require.Equal(t, []string{"system", "prompt"}, texts(im.apply(step0)))

	step1 := []fantasy.Message{sys, prompt, assistant1, tool1}
	require.Equal(t, []string{"system", "prompt", "a1", "t1"}, texts(im.apply(step1)))
	im.add(step1, queued1)

	step2 := []fantasy.Message{sys, prompt, assistant1, tool1, assistant2, tool2}
	require.Equal(t,
		[]string{"system", "prompt", "a1", "t1", "queued1", "a2", "t2"},
		texts(im.apply(step2)),
	)
	im.add(step2, queued2)

	step3 := append(step2, assistant1)
	require.Equal(t,
		[]string{"system", "prompt", "a1", "t1", "queued1", "a2", "t2", "queued2", "a1"},
		texts(im.apply(step3)),
	)
	require.Len(t, step3, 7, "apply must not modify its input")
}

func TestInjectedMessagesAddOnFirstStep(t *testing.T) {
	t.Parallel()

	im := newInjectedMessages()
	step0 := []fantasy.Message{fantasy.NewUserMessage("prompt")}
	im.add(step0, fantasy.NewUserMessage("queued"))

	step1 := append(step0, fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "a1"}}})
	got := im.apply(step1)
	require.Len(t, got, 3)
	require.Equal(t, "prompt", textOf(got[0]))
	require.Equal(t, "queued", textOf(got[1]))
	require.Equal(t, "a1", textOf(got[2]))
}

// scriptedModel is a fantasy.LanguageModel that records every prompt it
// receives and replies with a tool call for the first toolSteps steps, then
// with plain text.
type scriptedModel struct {
	mu        sync.Mutex
	prompts   [][]fantasy.Message
	toolName  string
	toolSteps int
}

func (m *scriptedModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, cloneFantasyMessages(call.Prompt))
	step := len(m.prompts) - 1
	m.mu.Unlock()

	return iter.Seq[fantasy.StreamPart](func(yield func(fantasy.StreamPart) bool) {
		if step < m.toolSteps {
			id := fmt.Sprintf("call_%d", step)
			parts := []fantasy.StreamPart{
				{Type: fantasy.StreamPartTypeToolInputStart, ID: id, ToolCallName: m.toolName},
				{Type: fantasy.StreamPartTypeToolInputEnd, ID: id},
				{Type: fantasy.StreamPartTypeToolCall, ID: id, ToolCallName: m.toolName, ToolCallInput: "{}"},
				{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls},
			}
			for _, part := range parts {
				if !yield(part) {
					return
				}
			}
			return
		}
		parts := []fantasy.StreamPart{
			{Type: fantasy.StreamPartTypeTextStart, ID: "text"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: "done"},
			{Type: fantasy.StreamPartTypeTextEnd, ID: "text"},
			{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
		}
		for _, part := range parts {
			if !yield(part) {
				return
			}
		}
	}), nil
}

func (m *scriptedModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *scriptedModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *scriptedModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *scriptedModel) Provider() string { return "scripted" }
func (m *scriptedModel) Model() string    { return "scripted" }

// TestQueuedPromptRunsAfterCurrentRun reproduces prompts that the user
// sends while the agent is mid-run: the queued prompt is not injected into
// the running turn but starts its own run once the current one finishes.
func TestQueuedPromptRunsAfterCurrentRun(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Queued prompt", t.TempDir())
	require.NoError(t, err)

	const queuedText = "please also update the README"

	var sessionAgent SessionAgent
	var queueOnce sync.Once
	queueTool := fantasy.NewAgentTool(
		"queue_prompt",
		"Test tool that simulates the user sending a prompt mid-run.",
		func(ctx context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			var runErr error
			queueOnce.Do(func() {
				_, runErr = sessionAgent.Run(ctx, SessionAgentCall{
					SessionID:      sess.ID,
					Prompt:         queuedText,
					NonInteractive: true,
				})
			})
			if runErr != nil {
				return fantasy.ToolResponse{}, runErr
			}
			return fantasy.NewTextResponse("ok"), nil
		},
	)

	model := &scriptedModel{toolName: "queue_prompt", toolSteps: 3}
	sessionAgent = testSessionAgent(env, model, model, "system", queueTool)

	_, err = sessionAgent.Run(t.Context(), SessionAgentCall{
		SessionID:      sess.ID,
		Prompt:         "start",
		NonInteractive: true,
	})
	require.NoError(t, err)

	require.Len(t, model.prompts, 5)

	countQueued := func(prompt []fantasy.Message) int {
		n := 0
		for _, msg := range prompt {
			if msg.Role == fantasy.MessageRoleUser && strings.Contains(textOf(msg), queuedText) {
				n++
			}
		}
		return n
	}

	for step := range 4 {
		require.Zero(t, countQueued(model.prompts[step]),
			"queued prompt must not be injected into the running turn at step %d", step)
	}
	last := model.prompts[4]
	require.Equal(t, 1, countQueued(last))
	require.Equal(t, fantasy.MessageRoleUser, last[len(last)-1].Role)
	require.Contains(t, textOf(last[len(last)-1]), queuedText)
}

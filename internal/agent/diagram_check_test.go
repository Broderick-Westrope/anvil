package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

const (
	brokenDiagramReply = "Flow:\n\n```mermaid\nsequenceDiagram\n    participant Off as offerings\n    A->>Off: buys\n```\n"
	fixedDiagramReply  = "```mermaid\nsequenceDiagram\n    participant Ofr as offerings\n    A->>Ofr: buys\n```\n"
)

// replyModel streams replies in order, repeating the last one, and records
// each step's prompt.
type replyModel struct {
	mu      sync.Mutex
	replies []string
	prompts [][]fantasy.Message
	// onStream, when set, runs at the start of each step with its index.
	onStream func(step int)
	// failStep, when positive, is the 1-based step that fails.
	failStep int
}

func (m *replyModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, cloneFantasyMessages(call.Prompt))
	step := len(m.prompts) - 1
	reply := m.replies[min(step, len(m.replies)-1)]
	onStream := m.onStream
	m.mu.Unlock()
	if onStream != nil {
		onStream(step)
	}
	if m.failStep == step+1 {
		return nil, errors.New("provider down")
	}
	return iter.Seq[fantasy.StreamPart](func(yield func(fantasy.StreamPart) bool) {
		for _, part := range []fantasy.StreamPart{
			{Type: fantasy.StreamPartTypeTextStart, ID: "text"},
			{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: reply},
			{Type: fantasy.StreamPartTypeTextEnd, ID: "text"},
			{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
		} {
			if !yield(part) {
				return
			}
		}
	}), nil
}

func (m *replyModel) steps() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prompts)
}

func (m *replyModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *replyModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *replyModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *replyModel) Provider() string { return "reply" }
func (m *replyModel) Model() string    { return "reply" }

func diagramFixes(t *testing.T, messages message.Service, sessionID string) int {
	t.Helper()
	msgs, err := messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	var fixes int
	for _, msg := range msgs {
		for _, inj := range msg.Injections() {
			if inj.Kind == message.InjectionKindDiagramFix {
				fixes++
			}
		}
	}
	return fixes
}

func TestReplyDiagramCheck(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		replies        []string
		checkDiagrams  bool
		nonInteractive bool
		await          bool
		submitAwait    bool
		queuePrompt    bool
		prompt         string
		holdDuringTurn bool
		failStep       int
		wantSteps      int
		wantFixes      int
	}{
		"a broken diagram gets one fix turn": {
			replies:       []string{brokenDiagramReply, fixedDiagramReply},
			checkDiagrams: true,
			wantSteps:     2,
			wantFixes:     1,
		},
		"a fix that still fails isn't asked again": {
			replies:       []string{brokenDiagramReply},
			checkDiagrams: true,
			wantSteps:     2,
			wantFixes:     1,
		},
		"diagrams that render aren't fixed": {
			replies:       []string{fixedDiagramReply},
			checkDiagrams: true,
			wantSteps:     1,
		},
		"off when diagrams are off": {
			replies:   []string{brokenDiagramReply},
			wantSteps: 1,
		},
		"off for non-interactive runs": {
			replies:        []string{brokenDiagramReply},
			checkDiagrams:  true,
			nonInteractive: true,
			wantSteps:      1,
		},
		"off for awaited turns, which return their own reply": {
			replies:       []string{brokenDiagramReply},
			checkDiagrams: true,
			await:         true,
			wantSteps:     1,
		},
		"off for awaited submissions, as a workflow's RunTurn makes": {
			replies:       []string{brokenDiagramReply},
			checkDiagrams: true,
			submitAwait:   true,
			wantSteps:     1,
		},
		"skipped when a hold waits for the turn": {
			replies:        []string{brokenDiagramReply},
			checkDiagrams:  true,
			holdDuringTurn: true,
			wantSteps:      1,
		},
		"a failed fix leaves the turn's success alone": {
			replies:       []string{brokenDiagramReply},
			checkDiagrams: true,
			failStep:      2,
			wantSteps:     2,
			wantFixes:     1,
		},
		"skipped for a diagram the user quoted": {
			replies:       []string{brokenDiagramReply},
			checkDiagrams: true,
			prompt:        "why doesn't this render?\n\nsequenceDiagram\n    participant Off as offerings\n    A->>Off: buys\n",
			wantSteps:     1,
		},
		"skipped when the user queued a prompt during the turn": {
			replies:       []string{brokenDiagramReply, fixedDiagramReply},
			checkDiagrams: true,
			queuePrompt:   true,
			wantSteps:     2,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			sess, err := env.sessions.Create(t.Context(), "Diagrams", t.TempDir())
			require.NoError(t, err)
			model := &replyModel{replies: tc.replies, failStep: tc.failStep}
			small := &replyModel{replies: []string{"title"}}
			a := NewSessionAgent(SessionAgentOptions{
				LargeModel:    Model{Model: model, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}},
				SmallModel:    Model{Model: small, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}},
				SystemPrompt:  "system",
				IsYolo:        true,
				Sessions:      env.sessions,
				Messages:      env.messages,
				CheckDiagrams: tc.checkDiagrams,
			})

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if tc.holdDuringTurn {
				model.onStream = func(step int) {
					if step == 0 {
						t.Cleanup(a.(*sessionAgent).admission.reserve([]string{sess.ID}))
					}
				}
			}
			if tc.queuePrompt {
				model.onStream = func(step int) {
					if step == 0 {
						_, err := a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "and another"})
						require.NoError(t, err)
					}
				}
			}
			call := SessionAgentCall{SessionID: sess.ID, Prompt: cmp.Or(tc.prompt, "draw it"), NonInteractive: tc.nonInteractive, Await: tc.await}
			if tc.submitAwait {
				sa := a.(*sessionAgent)
				call.state = &runState{}
				result, err := sa.admission.submitAwait(ctx, sess.ID, submission{prompt: call.Prompt, run: func(ctx context.Context) (*fantasy.AgentResult, error) {
					return sa.runOwned(ctx, call)
				}})
				require.NoError(t, err)
				require.Contains(t, result.Response.Content.Text(), "participant Off", "the submitter gets its own reply")
			} else {
				_, err = a.Run(ctx, call)
				require.NoError(t, err)
			}
			a.(*sessionAgent).WaitBackgroundJobs()

			require.Equal(t, tc.wantSteps, model.steps())
			require.Equal(t, tc.wantFixes, diagramFixes(t, env.messages, sess.ID))
			if tc.wantFixes == 0 {
				return
			}
			last := model.prompts[1]
			fixPrompt := textOf(last[len(last)-1])
			require.Contains(t, fixPrompt, `<injected_message source="harness">`)
			require.Contains(t, fixPrompt, "Diagram 1, line 6:")
			require.Contains(t, fixPrompt, "got 'off'")
		})
	}
}

func TestReplyDiagramFixIsCancelledWithTheRun(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Diagrams", t.TempDir())
	require.NoError(t, err)
	model := &replyModel{replies: []string{brokenDiagramReply, fixedDiagramReply}}
	cfg := catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:    Model{Model: model, CatwalkCfg: cfg},
		SmallModel:    Model{Model: &replyModel{replies: []string{"title"}}, CatwalkCfg: cfg},
		SystemPrompt:  "system",
		IsYolo:        true,
		Sessions:      env.sessions,
		Messages:      env.messages,
		CheckDiagrams: true,
	})
	model.onStream = func(step int) {
		if step == 1 {
			a.Cancel(sess.ID)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err = a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "draw it"})
	a.(*sessionAgent).WaitBackgroundJobs()

	require.NoError(t, err, "the first reply succeeded; the fix is best-effort")
	require.Equal(t, 2, model.steps())
	require.False(t, a.IsSessionBusy(sess.ID))
}

func TestReplyDiagramFixLeavesWakeStateAlone(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Diagrams", t.TempDir())
	require.NoError(t, err)
	model := &replyModel{replies: []string{brokenDiagramReply, fixedDiagramReply}}
	cfg := catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:    Model{Model: model, CatwalkCfg: cfg},
		SmallModel:    Model{Model: &replyModel{replies: []string{"title"}}, CatwalkCfg: cfg},
		SystemPrompt:  "system",
		IsYolo:        true,
		Sessions:      env.sessions,
		Messages:      env.messages,
		CheckDiagrams: true,
	}).(*sessionAgent)
	model.onStream = func(step int) {
		if step == 0 {
			a.wakeSuppressed.Set(sess.ID, true)
			a.wakeCounts.Set(sess.ID, 2)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err = a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "draw it"})
	require.NoError(t, err)
	a.WaitBackgroundJobs()

	require.Equal(t, 2, model.steps())
	suppressed, _ := a.wakeSuppressed.Get(sess.ID)
	require.True(t, suppressed, "a harness fix isn't the user acting")
	require.Equal(t, 2, a.wakeCount(sess.ID))
}

func newDiagramFixAgent(env fakeEnv, model *replyModel, agentTools ...fantasy.AgentTool) *sessionAgent {
	cfg := catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}
	return NewSessionAgent(SessionAgentOptions{
		LargeModel:    Model{Model: model, CatwalkCfg: cfg},
		SmallModel:    Model{Model: &replyModel{replies: []string{"title"}}, CatwalkCfg: cfg},
		SystemPrompt:  "system",
		IsYolo:        true,
		Sessions:      env.sessions,
		Messages:      env.messages,
		Tools:         agentTools,
		CheckDiagrams: true,
	}).(*sessionAgent)
}

func TestReplyDiagramFixNamesValidateMermaidOnlyWhenTheAgentHasIt(t *testing.T) {
	t.Parallel()
	for _, hasTool := range []bool{false, true} {
		t.Run(fmt.Sprintf("has tool=%t", hasTool), func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			sess, err := env.sessions.Create(t.Context(), "Diagrams", t.TempDir())
			require.NoError(t, err)
			model := &replyModel{replies: []string{brokenDiagramReply, fixedDiagramReply}}
			var agentTools []fantasy.AgentTool
			if hasTool {
				agentTools = append(agentTools, stepTool(tools.ValidateMermaidToolName, func(context.Context, int) fantasy.ToolResponse {
					return fantasy.NewTextResponse("ok")
				}))
			}
			a := newDiagramFixAgent(env, model, agentTools...)

			_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "draw it"})
			require.NoError(t, err)
			a.WaitBackgroundJobs()

			require.Equal(t, 2, model.steps())
			last := model.prompts[1]
			fixPrompt := textOf(last[len(last)-1])
			require.Contains(t, fixPrompt, diagramFixText, "the request itself is always there")
			require.Equal(t, hasTool, strings.Contains(fixPrompt, diagramFixText+diagramFixCheckText+"\n\n"))
			require.Contains(t, fixPrompt, "Diagram 1, line 6:")
		})
	}
}

// Not parallel: it captures the default logger.
func TestReplyDiagramFixLogsAFailureButNotACancel(t *testing.T) {
	tests := map[string]struct {
		cancelFix bool
		wantLog   bool
	}{
		"a failed fix is logged":       {wantLog: true},
		"a cancelled fix isn't logged": {cancelFix: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			logs := &lockedBuffer{}
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			env := testEnv(t)
			sess, err := env.sessions.Create(t.Context(), "Diagrams", t.TempDir())
			require.NoError(t, err)
			model := &replyModel{replies: []string{brokenDiagramReply}, failStep: 2}
			a := newDiagramFixAgent(env, model)
			if tc.cancelFix {
				model.onStream = func(step int) {
					if step == 1 {
						a.Cancel(sess.ID)
					}
				}
			}

			_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "draw it"})
			require.NoError(t, err)
			a.WaitBackgroundJobs()

			require.Equal(t, 2, model.steps())
			require.Equal(t, tc.wantLog, strings.Contains(logs.String(), "Diagram fix turn failed"))
		})
	}
}

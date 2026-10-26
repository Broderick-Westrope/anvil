package agent

import (
	"context"
	"fmt"
	"iter"
	"sync"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
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
		queuePrompt    bool
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
			model := &replyModel{replies: tc.replies}
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
			if tc.queuePrompt {
				model.onStream = func(step int) {
					if step == 0 {
						_, err := a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "and another"})
						require.NoError(t, err)
					}
				}
			}
			_, err = a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "draw it", NonInteractive: tc.nonInteractive, Await: tc.await})
			require.NoError(t, err)
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

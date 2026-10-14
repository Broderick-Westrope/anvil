package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// fakeBackend scripts primitive results so host tests run without models.
type fakeBackend struct {
	mu sync.Mutex

	skills []string
	agents []string

	turns   []workflow.TurnCall
	turnFn  func(ctx context.Context, call workflow.TurnCall) (string, error)
	agentFn func(ctx context.Context, call workflow.AgentCall) (workflow.AgentResult, error)
	bashFn  func(ctx context.Context, call workflow.BashCall) (workflow.BashResult, error)
	askFn   func(ctx context.Context, question string, options []string) (string, error)

	newSessions  int
	sessionCalls []workflow.SessionCall
	statuses     [][2]string
	focused      []string
	warnings     []string
	holds        [][]string
	stopJobs     []bool
	holdErr      error
	released     int
	bashed       []string
}

func (f *fakeBackend) bashCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bashed
}

func (f *fakeBackend) releases() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.released
}

func (f *fakeBackend) HasSkill(name string) bool { return slices.Contains(f.skills, name) }
func (f *fakeBackend) HasAgent(name string) bool { return slices.Contains(f.agents, name) }

func (f *fakeBackend) Skill(name string) (workflow.SkillContent, bool) {
	if !f.HasSkill(name) {
		return workflow.SkillContent{}, false
	}
	return workflow.SkillContent{Body: "body of " + name, Location: "/skills/" + name}, true
}

func (f *fakeBackend) Turn(ctx context.Context, call workflow.TurnCall) (string, error) {
	f.mu.Lock()
	f.turns = append(f.turns, call)
	fn := f.turnFn
	f.mu.Unlock()
	if fn == nil {
		return "ok", nil
	}
	return fn(ctx, call)
}

func (f *fakeBackend) NewSession(_ context.Context, call workflow.SessionCall) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newSessions++
	f.sessionCalls = append(f.sessionCalls, call)
	return "new-" + string(rune('0'+f.newSessions)), nil
}

func (f *fakeBackend) Agent(ctx context.Context, call workflow.AgentCall) (workflow.AgentResult, error) {
	if f.agentFn == nil {
		return workflow.AgentResult{Text: call.Agent + " done"}, nil
	}
	return f.agentFn(ctx, call)
}

func (f *fakeBackend) Bash(ctx context.Context, call workflow.BashCall) (workflow.BashResult, error) {
	f.mu.Lock()
	f.bashed = append(f.bashed, call.Cmd)
	f.mu.Unlock()
	if f.bashFn == nil {
		return workflow.BashResult{Stdout: "ran " + call.Cmd}, nil
	}
	return f.bashFn(ctx, call)
}

func (f *fakeBackend) Ask(ctx context.Context, question string, options []string) (string, error) {
	if f.askFn == nil {
		return "yes", nil
	}
	return f.askFn(ctx, question, options)
}

func (f *fakeBackend) Status(step, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, [2]string{step, detail})
}

func (f *fakeBackend) Focus(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focused = append(f.focused, sessionID)
}

func (f *fakeBackend) Warn(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warnings = append(f.warnings, msg)
}

func (f *fakeBackend) Hold(_ context.Context, call workflow.HoldCall) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.holdErr; err != nil {
		f.holdErr = nil
		return nil, err
	}
	f.holds = append(f.holds, slices.Clone(call.SessionIDs))
	f.stopJobs = append(f.stopJobs, call.StopJobs)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.released++
	}, nil
}

// memRecorder keeps events in memory.
type memRecorder struct {
	mu     sync.Mutex
	events []workflow.Event
}

func (m *memRecorder) Record(_ context.Context, ev workflow.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
	return nil
}

func (m *memRecorder) primitives(phase workflow.Phase) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, ev := range m.events {
		if ev.Phase == phase {
			out = append(out, ev.Primitive)
		}
	}
	return out
}

type harness struct {
	backend  *fakeBackend
	recorder *memRecorder
	dir      string
	// references are the approved files reference() serves.
	references map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{
		backend:  &fakeBackend{agents: []string{"reviewer", "devils-advocate"}, skills: []string{"go-style"}},
		recorder: &memRecorder{},
		dir:      t.TempDir(),
	}
}

func (h *harness) newRun(t *testing.T, src string) *workflow.Run {
	t.Helper()
	path := filepath.Join(h.dir, "workflow.js")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))
	script, err := workflow.Load(path)
	require.NoError(t, err)
	return workflow.NewRun(script, h.backend, h.recorder, workflow.Options{
		InvokingSessionID: "invoker",
		Tmpdir:            filepath.Join(h.dir, "tmp"),
		References:        h.references,
	})
}

// run executes src and returns main's result as decoded JSON.
func (h *harness) run(t *testing.T, src, raw string) (any, error) {
	t.Helper()
	out, err := h.newRun(t, src).Execute(t.Context(), workflow.Input{Raw: raw})
	if err != nil {
		return nil, err
	}
	var v any
	if len(out) > 0 {
		require.NoError(t, json.Unmarshal(out, &v))
	}
	return v, nil
}

var errBoom = errors.New("boom")

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

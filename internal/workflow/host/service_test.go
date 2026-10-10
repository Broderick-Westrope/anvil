package host_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/workflow/host"
)

type serviceHarness struct {
	fixture *branchfixture.Fixture
	svc     *host.Service
	store   *host.Store
	events  <-chan pubsub.Event[host.Event]
	session string
	dir     string
	yolo    bool
}

func newServiceHarness(t *testing.T, yolo bool, opts ...func(*host.Deps)) *serviceHarness {
	t.Helper()
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	store := host.NewStore(f.Queries, t.TempDir(), "inst-test")
	h := &serviceHarness{fixture: f, store: store, dir: t.TempDir(), yolo: yolo}
	deps := host.Deps{
		Coordinator: f.Coordinator,
		Sessions:    f.Sessions,
		Store:       store,
		WorkingDir:  f.Config.WorkingDir(),
		SkipApproval: func() bool {
			return h.yolo
		},
	}
	for _, opt := range opts {
		opt(&deps)
	}
	h.svc = host.NewService(f.Context, deps)
	h.events = h.svc.Subscribe(f.Context)
	s, err := f.Workspace.CreateSession(f.Context, "invoker")
	require.NoError(t, err)
	h.session = s.ID
	return h
}

func (h *serviceHarness) write(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(h.dir, "workflow.js")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))
	return path
}

func (h *serviceHarness) start(t *testing.T, path, raw string) {
	t.Helper()
	require.NoError(t, h.svc.Start(h.fixture.Context, host.Invocation{
		CommandID: "project:demo", CommandName: "demo", Source: "project",
		ScriptPath: path, SessionID: h.session, Raw: raw,
	}))
}

// next returns the next event of kind, skipping others.
func (h *serviceHarness) next(t *testing.T, kind host.EventKind) host.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev.Payload.Kind == kind {
				return ev.Payload
			}
		case <-timeout:
			t.Fatalf("no %s event", kind)
		}
	}
}

func TestServiceRunsWorkflowAndRecordsIt(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main(input) {
  status("echo", input.raw)
  return bash("echo " + input.raw).stdout.trim()
}`)
	h.start(t, path, "hello")

	require.Equal(t, "echo", h.next(t, host.EventStatus).Step)
	done := h.next(t, host.EventFinished)
	require.Equal(t, host.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"hello"`, string(done.Result))

	run, ok, err := h.store.GetRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, host.StatusCompleted, run.Status)
	events, err := h.store.ListEvents(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
}

func TestServiceAsksForApprovalUntilScriptChanges(t *testing.T) {
	h := newServiceHarness(t, false)
	path := h.write(t, `function main() { return 1 }`)

	h.start(t, path, "")
	req := h.next(t, host.EventApproval)
	require.Empty(t, req.Approval.PreviousSource)
	require.Contains(t, req.Approval.Source, "function main")
	require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
	require.Equal(t, host.StatusCompleted, h.next(t, host.EventFinished).Status)

	h.start(t, path, "")
	require.Equal(t, host.StatusCompleted, h.next(t, host.EventFinished).Status)

	path = h.write(t, `function main() { return 2 }`)
	h.start(t, path, "")
	req = h.next(t, host.EventApproval)
	require.Contains(t, req.Approval.PreviousSource, "return 1")
	require.NoError(t, h.svc.Respond(req.RequestID, "deny"))
	done := h.next(t, host.EventFinished)
	require.Equal(t, host.StatusDenied, done.Status)
	require.Empty(t, done.RunID, "a denied script never gets a run")
}

func TestServiceRefusesMissingRequirementsBeforeRunning(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `requires({agents: ["ghost"]}); function main() { bash("touch should-not-exist") }`)
	err := h.svc.Start(h.fixture.Context, host.Invocation{CommandID: "x", ScriptPath: path, SessionID: h.session})
	require.ErrorContains(t, err, `agent "ghost" not found`)
}

func TestServiceAllowsOneRunPerSession(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { return ask("continue?") }`)
	h.start(t, path, "")
	ask := h.next(t, host.EventAsk)
	err := h.svc.Start(h.fixture.Context, host.Invocation{CommandID: "x", ScriptPath: path, SessionID: h.session})
	require.ErrorIs(t, err, host.ErrRunInProgress)
	require.NoError(t, h.svc.Respond(ask.RequestID, "yes"))
	done := h.next(t, host.EventFinished)
	require.JSONEq(t, `"yes"`, string(done.Result))
}

func TestServiceAbortEndsRun(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { ask("wait") }`)
	h.start(t, path, "")
	h.next(t, host.EventAsk)
	require.NoError(t, h.svc.Abort(h.session))
	require.Equal(t, host.StatusAborted, h.next(t, host.EventFinished).Status)
}

func TestServiceTurnRunsInSession(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "model says hi"})
	path := h.write(t, `function main() { return session.current().turn("hello") }`)
	h.start(t, path, "")
	done := h.next(t, host.EventFinished)
	require.Equal(t, host.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"model says hi"`, string(done.Result))
}

func TestServiceAgentReturnsStructuredOutput(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(
		branchfixture.Response{ToolName: "submit_result", ToolInput: `{"kind":"clean"}`},
		branchfixture.Response{Text: "done"},
	)
	path := h.write(t, `requires({agents: ["reviewer"]})
const S = {type: "object", properties: {kind: {type: "string"}}, required: ["kind"]}
function main() { return agent("reviewer", "review", {schema: S}).kind }`)
	h.start(t, path, "")
	done := h.next(t, host.EventFinished)
	require.Equal(t, host.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"clean"`, string(done.Result))
}

func TestServiceBashRunsInEmbeddedShellWithCwd(t *testing.T) {
	h := newServiceHarness(t, true)
	sub := filepath.Join(h.dir, "sub dir")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	path := h.write(t, `function main(input) { return bash("pwd", {cwd: input.raw}).stdout.trim() }`)
	h.start(t, path, sub)
	done := h.next(t, host.EventFinished)
	require.Equal(t, host.StatusCompleted, done.Status, done.Message)
	got, err := filepath.EvalSymlinks(sub)
	require.NoError(t, err)
	var out string
	require.NoError(t, json.Unmarshal(done.Result, &out))
	out, err = filepath.EvalSymlinks(out)
	require.NoError(t, err)
	require.Equal(t, got, out)
}

func TestServiceTurnIsRecordedAsWorkflowTurn(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "ok"})
	path := h.write(t, `function main() { return session.current().turn("do the thing") }`)
	h.start(t, path, "")
	done := h.next(t, host.EventFinished)
	require.Equal(t, host.StatusCompleted, done.Status, done.Message)

	msgs, err := h.fixture.Workspace.ListUserMessages(h.fixture.Context, h.session)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	command, prompt, ok := commands.ParseWorkflowTurnXML(msgs[0].Content().Text)
	require.True(t, ok, msgs[0].Content().Text)
	require.Equal(t, "/demo", command)
	require.Equal(t, "do the thing", prompt)
}

func TestServiceAgentPublishesItsSession(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "looks fine"})
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() { return agent("reviewer", "review") }`)
	h.start(t, path, "")

	start := h.next(t, host.EventAgent)
	require.Equal(t, "demo", start.Command)
	require.Equal(t, h.session, start.SessionID)
	require.Equal(t, "reviewer", start.Step)
	require.Empty(t, start.Detail, "no model was pinned")
	require.Equal(t, host.StatusRunning, start.Status)
	require.NotEmpty(t, start.Target)

	end := h.next(t, host.EventAgent)
	require.Equal(t, start.Target, end.Target)
	require.Equal(t, "reviewer", end.Step)
	require.Equal(t, host.StatusCompleted, end.Status)
	require.Equal(t, host.StatusCompleted, h.next(t, host.EventFinished).Status)

	child, err := h.fixture.Workspace.GetSession(h.fixture.Context, start.Target)
	require.NoError(t, err)
	require.Equal(t, h.session, child.ParentSessionID)
	require.Equal(t, "/demo: reviewer", child.Title)
}

func TestServiceAgentPublishesFailure(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() { return agent("reviewer", "review", {model: "nope/nope"}) }`)
	h.start(t, path, "")

	start := h.next(t, host.EventAgent)
	require.Equal(t, "nope/nope", start.Detail)
	end := h.next(t, host.EventAgent)
	require.Equal(t, start.Target, end.Target)
	require.Equal(t, host.StatusFailed, end.Status)
	require.Contains(t, end.Message, "nope/nope")
	require.Equal(t, host.StatusFailed, h.next(t, host.EventFinished).Status)
}

func TestServiceParallelAgentsEachPublishTheirSession(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "a"}, branchfixture.Response{Text: "b"})
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() { parallel([{agent: "reviewer", prompt: "a"}, {agent: "reviewer", prompt: "b"}]) }`)
	h.start(t, path, "")

	running, ended := map[string]bool{}, map[string]bool{}
	for range 4 {
		ev := h.next(t, host.EventAgent)
		if ev.Status == host.StatusRunning {
			running[ev.Target] = true
		} else {
			ended[ev.Target] = true
		}
	}
	require.Len(t, running, 2)
	require.Equal(t, running, ended)
	require.Equal(t, host.StatusCompleted, h.next(t, host.EventFinished).Status)
}

func TestServiceShutdownInterruptsRunsAndRefusesNewOnes(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { try { ask("wait") } catch (e) {} ; bash("echo after") }`)
	h.start(t, path, "")
	ask := h.next(t, host.EventAsk)

	ctx, cancel := context.WithTimeout(h.fixture.Context, 5*time.Second)
	defer cancel()
	require.NoError(t, h.svc.Shutdown(ctx))

	done := h.next(t, host.EventFinished)
	require.Equal(t, host.StatusInterrupted, done.Status)
	run, ok, err := h.store.GetRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, host.StatusInterrupted, run.Status)
	require.Error(t, h.svc.Respond(ask.RequestID, "late"))
	err = h.svc.Start(h.fixture.Context, host.Invocation{CommandID: "x", ScriptPath: path, SessionID: h.session})
	require.ErrorIs(t, err, host.ErrShuttingDown)
}

func TestServiceExclusiveRefusesThenStopsSessionJobs(t *testing.T) {
	h := newServiceHarness(t, true)
	mgr := shell.GetBackgroundShellManager()
	bs, err := mgr.Start(h.fixture.Context, h.dir, nil, "sleep 30", "watcher")
	require.NoError(t, err)
	jobID, err := mgr.Publish(h.fixture.Context, bs.ID(), shell.PublishOptions{SessionID: h.session, Origin: shell.OriginExplicit})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Kill(jobID) })

	path := h.write(t, `function main() {
  let refused
  try { workflow.exclusive(() => 1) } catch (e) { refused = e instanceof JobsRunning && e.jobs[0].command }
  workflow.exclusive(() => 1, {stopJobs: true})
  return refused
}`)
	h.start(t, path, "")
	done := h.next(t, host.EventFinished)
	require.Equal(t, host.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"sleep 30"`, string(done.Result))
	for _, job := range mgr.ListBySession(h.session) {
		require.True(t, job.Done, "job %s still running", job.ID)
	}
}

func TestServiceRepublishesUnansweredRequestsUntilAnswered(t *testing.T) {
	h := newServiceHarness(t, true, func(d *host.Deps) { d.RepublishInterval = 20 * time.Millisecond })
	path := h.write(t, `function main() { return ask("still there?") }`)
	h.start(t, path, "")
	first := h.next(t, host.EventAsk)
	again := h.next(t, host.EventAsk)
	require.Equal(t, first.RequestID, again.RequestID)
	require.NoError(t, h.svc.Respond(first.RequestID, "yes"))
	done := h.next(t, host.EventFinished)
	require.JSONEq(t, `"yes"`, string(done.Result))
	select {
	case ev := <-h.events:
		require.NotEqual(t, host.EventAsk, ev.Payload.Kind, "answered request was republished")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServiceRefusesAWorkflowFromASessionAnotherRunUses(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { const s = session.new({}); workflow.focus(s); return ask("hold on") }`)
	h.start(t, path, "")
	focus := h.next(t, host.EventFocus)
	ask := h.next(t, host.EventAsk)
	err := h.svc.Start(h.fixture.Context, host.Invocation{CommandID: "x", ScriptPath: path, SessionID: focus.Target})
	require.ErrorIs(t, err, host.ErrRunInProgress)
	require.NoError(t, h.svc.Respond(ask.RequestID, "ok"))
	h.next(t, host.EventFinished)
}

package workflowhost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
)

type serviceHarness struct {
	fixture *branchfixture.Fixture
	svc     *workflowhost.Service
	store   *workflowhost.Store
	events  <-chan pubsub.Event[workflowhost.Event]
	session string
	dir     string
	yolo    bool
}

func newServiceHarness(t *testing.T, yolo bool, opts ...func(*workflowhost.Deps)) *serviceHarness {
	t.Helper()
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	store := workflowhost.NewStore(f.Queries, t.TempDir(), "inst-test")
	h := &serviceHarness{fixture: f, store: store, dir: t.TempDir(), yolo: yolo}
	deps := workflowhost.Deps{
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
	h.svc = workflowhost.NewService(f.Context, deps)
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
	require.NoError(t, h.svc.Start(h.fixture.Context, workflowhost.Invocation{
		CommandID: "project:demo", CommandName: "demo", Source: "project",
		ScriptPath: path, SessionID: h.session, Raw: raw,
	}))
}

// next returns the next event of kind, skipping others.
func (h *serviceHarness) next(t *testing.T, kind workflowhost.EventKind) workflowhost.Event {
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

	require.Equal(t, "echo", h.next(t, workflowhost.EventStatus).Step)
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"hello"`, string(done.Result))

	run, ok, err := h.store.GetRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, workflowhost.StatusCompleted, run.Status)
	events, err := h.store.ListEvents(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
}

func TestServiceAsksForApprovalUntilScriptChanges(t *testing.T) {
	h := newServiceHarness(t, false)
	path := h.write(t, `function main() { return 1 }`)

	h.start(t, path, "")
	req := h.next(t, workflowhost.EventApproval)
	require.Len(t, req.Approval.Files, 1)
	require.Equal(t, "workflow.js", req.Approval.Files[0].Path)
	require.Equal(t, workflowhost.FileAdded, req.Approval.Files[0].Status)
	require.Empty(t, req.Approval.Files[0].PreviousSource)
	require.Contains(t, req.Approval.Files[0].Source, "function main")
	require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)

	h.start(t, path, "")
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)

	path = h.write(t, `function main() { return 2 }`)
	h.start(t, path, "")
	req = h.next(t, workflowhost.EventApproval)
	require.Equal(t, workflowhost.FileChanged, req.Approval.Files[0].Status)
	require.Contains(t, req.Approval.Files[0].PreviousSource, "return 1")
	require.NoError(t, h.svc.Respond(req.RequestID, "deny"))
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusDenied, done.Status)
	require.Empty(t, done.RunID, "a denied script never gets a run")
}

func TestServiceRespondAcceptsOnlyOfferedAnswers(t *testing.T) {
	h := newServiceHarness(t, false)
	path := h.write(t, `function main() { return ask("which?", {options: ["a", "b"]}) }`)
	h.start(t, path, "")

	approval := h.next(t, workflowhost.EventApproval)
	require.Error(t, h.svc.Respond(approval.RequestID, "yes"))
	require.NoError(t, h.svc.Respond(approval.RequestID, "approve"))

	ask := h.next(t, workflowhost.EventAsk)
	require.Error(t, h.svc.Respond(ask.RequestID, "c"))
	require.NoError(t, h.svc.Respond(ask.RequestID, "b"))
	done := h.next(t, workflowhost.EventFinished)
	require.JSONEq(t, `"b"`, string(done.Result))
}

func TestServiceApprovalCoversTheWholeCommandDirectory(t *testing.T) {
	h := newServiceHarness(t, false)
	writeFile(t, filepath.Join(h.dir, "references", "guide.md"), "be careful")
	writeFile(t, filepath.Join(h.dir, "a-first.md"), "sorted before the script")
	writeFile(t, filepath.Join(h.dir, ".git", "HEAD"), "ref: main")
	writeFile(t, filepath.Join(h.dir, "node_modules", "dep", "index.js"), "module.exports = 1")
	path := h.write(t, `function main() { return reference("references/guide.md") }`)

	h.start(t, path, "")
	req := h.next(t, workflowhost.EventApproval)
	var paths []string
	for _, f := range req.Approval.Files {
		paths = append(paths, f.Path)
		require.Equal(t, workflowhost.FileAdded, f.Status, f.Path)
	}
	require.Equal(t, []string{"workflow.js", "a-first.md", filepath.Join("references", "guide.md")}, paths,
		"script first, then the rest by path, skipping dot directories and node_modules")
	require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)

	writeFile(t, filepath.Join(h.dir, ".git", "HEAD"), "ref: other")
	writeFile(t, filepath.Join(h.dir, "node_modules", "dep", "index.js"), "module.exports = 2")
	h.start(t, path, "")
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status, "an unchanged bundle runs without asking")

	writeFile(t, filepath.Join(h.dir, "references", "guide.md"), "be reckless")
	require.NoError(t, os.Remove(filepath.Join(h.dir, "a-first.md")))
	writeFile(t, filepath.Join(h.dir, "new.sh"), "rm -rf /")
	h.start(t, path, "")
	req = h.next(t, workflowhost.EventApproval)
	got := map[string]workflowhost.ApprovalFile{}
	for _, f := range req.Approval.Files {
		got[f.Path] = f
	}
	require.Equal(t, "workflow.js", req.Approval.Files[0].Path)
	require.Equal(t, workflowhost.FileUnchanged, got["workflow.js"].Status)
	require.Equal(t, workflowhost.FileRemoved, got["a-first.md"].Status)
	require.Equal(t, "sorted before the script", got["a-first.md"].PreviousSource)
	require.Equal(t, workflowhost.FileAdded, got["new.sh"].Status)
	guide := got[filepath.Join("references", "guide.md")]
	require.Equal(t, workflowhost.FileChanged, guide.Status)
	require.Equal(t, "be careful", guide.PreviousSource)
	require.Equal(t, "be reckless", guide.Source)
	require.NoError(t, h.svc.Respond(req.RequestID, "deny"))
	require.Equal(t, workflowhost.StatusDenied, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceApprovalFollowsASymlinkedCommandDirectory(t *testing.T) {
	h := newServiceHarness(t, false)
	target := t.TempDir()
	writeFile(t, filepath.Join(target, "workflow.js"), `function main() { return 1 }`)
	link := filepath.Join(h.dir, "linked")
	require.NoError(t, os.Symlink(target, link))
	path := filepath.Join(link, "workflow.js")

	h.start(t, path, "")
	req := h.next(t, workflowhost.EventApproval)
	require.Len(t, req.Approval.Files, 1)
	require.Equal(t, "workflow.js", req.Approval.Files[0].Path)
	require.Contains(t, req.Approval.Files[0].Source, "return 1")
	require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)

	writeFile(t, filepath.Join(target, "workflow.js"), `function main() { return 2 }`)
	h.start(t, path, "")
	req = h.next(t, workflowhost.EventApproval)
	require.Equal(t, workflowhost.FileChanged, req.Approval.Files[0].Status)
	require.NoError(t, h.svc.Respond(req.RequestID, "deny"))
	require.Equal(t, workflowhost.StatusDenied, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceReferenceServesTheApprovedContentAfterAnEdit(t *testing.T) {
	h := newServiceHarness(t, false)
	ref := filepath.Join(h.dir, "boundaries.md")
	writeFile(t, ref, "approved")
	path := h.write(t, `function main(input) {
  bash("echo edited > '" + input.raw + "'")
  return reference("boundaries.md")
}`)

	h.start(t, path, ref)
	req := h.next(t, workflowhost.EventApproval)
	require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"approved"`, string(done.Result))
}

func TestServiceReferenceServesTheBundleWhenApprovalIsSkipped(t *testing.T) {
	h := newServiceHarness(t, true)
	ref := filepath.Join(h.dir, "boundaries.md")
	writeFile(t, ref, "at start")
	path := h.write(t, `function main(input) {
  bash("echo edited > '" + input.raw + "'")
  return reference("boundaries.md")
}`)

	h.start(t, path, ref)
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"at start"`, string(done.Result))
}

func TestServiceReferenceCantReadASymlinkEscapingTheDirectory(t *testing.T) {
	h := newServiceHarness(t, true)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.md"), "secret")
	require.NoError(t, os.Symlink(outside, filepath.Join(h.dir, "linkdir")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(h.dir, "escape.md")))
	path := h.write(t, `function main(input) {
  const out = []
  for (const p of ["linkdir/secret.md", "escape.md"]) {
    try { out.push(reference(p)) } catch (e) { out.push(e.message) }
  }
  return out
}`)

	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `["reference \"linkdir/secret.md\" not found", "reference \"escape.md\" not found"]`, string(done.Result))
}

func TestServiceRefusesAScriptSymlinkedOutsideItsDirectory(t *testing.T) {
	h := newServiceHarness(t, true)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "real.js"), `function main() { bash("touch should-not-exist") }`)
	path := filepath.Join(h.dir, "workflow.js")
	require.NoError(t, os.Symlink(filepath.Join(outside, "real.js"), path))

	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusFailed, done.Status)
	require.Contains(t, done.Message, "doesn't contain the script")
	require.Empty(t, done.RunID)
}

func TestServiceApprovalShowsLargeAndBinaryFilesByHashOnly(t *testing.T) {
	h := newServiceHarness(t, false)
	big := strings.Repeat("x", 256*1024+1)
	atLimit := strings.Repeat("y", 256*1024)
	writeFile(t, filepath.Join(h.dir, "big.txt"), big)
	writeFile(t, filepath.Join(h.dir, "limit.txt"), atLimit)
	writeFile(t, filepath.Join(h.dir, "tool.bin"), "ELF\x00one")
	path := h.write(t, `function main() { return 1 }`)
	sha := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}

	h.start(t, path, "")
	req := h.next(t, workflowhost.EventApproval)
	got := map[string]workflowhost.ApprovalFile{}
	for _, f := range req.Approval.Files {
		got[f.Path] = f
	}
	require.Empty(t, got["big.txt"].Source)
	require.Equal(t, int64(len(big)), got["big.txt"].Size)
	require.Equal(t, sha(big), got["big.txt"].Hash)
	require.Equal(t, "over 256 KiB", got["big.txt"].Omitted)
	require.Empty(t, got["tool.bin"].Source)
	require.Equal(t, sha("ELF\x00one"), got["tool.bin"].Hash)
	require.Equal(t, "binary", got["tool.bin"].Omitted)
	require.Equal(t, atLimit, got["limit.txt"].Source)
	require.Empty(t, got["limit.txt"].Omitted)
	require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)

	writeFile(t, filepath.Join(h.dir, "tool.bin"), "ELF\x00two")
	h.start(t, path, "")
	req = h.next(t, workflowhost.EventApproval)
	for _, f := range req.Approval.Files {
		if f.Path == "tool.bin" {
			require.Equal(t, workflowhost.FileChanged, f.Status)
			require.Equal(t, sha("ELF\x00two"), f.Hash)
		} else {
			require.Equal(t, workflowhost.FileUnchanged, f.Status, f.Path)
		}
	}
	require.NoError(t, h.svc.Respond(req.RequestID, "deny"))
	require.Equal(t, workflowhost.StatusDenied, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceRecordsTheBundleHashOfEachRun(t *testing.T) {
	h := newServiceHarness(t, true)
	writeFile(t, filepath.Join(h.dir, "guide.md"), "one")
	path := h.write(t, `function main() { return 1 }`)
	runOnce := func() workflowhost.RunRecord {
		h.start(t, path, "")
		done := h.next(t, workflowhost.EventFinished)
		require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
		run, ok, err := h.store.GetRun(h.fixture.Context, done.RunID)
		require.NoError(t, err)
		require.True(t, ok)
		return run
	}

	first := runOnce()
	require.NotEmpty(t, first.BundleHash)
	writeFile(t, filepath.Join(h.dir, "guide.md"), "two")
	second := runOnce()
	require.Equal(t, first.ScriptHash, second.ScriptHash)
	require.NotEqual(t, first.BundleHash, second.BundleHash)
}

func TestServiceReapprovalReplacesThePreviousApproval(t *testing.T) {
	h := newServiceHarness(t, false)
	approveRun := func(path string) workflowhost.Event {
		h.start(t, path, "")
		req := h.next(t, workflowhost.EventApproval)
		require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
		require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)
		return req
	}
	path := h.write(t, `function main() { return 1 }`)
	approveRun(path)
	path = h.write(t, `function main() { return 2 }`)
	approveRun(path)

	path = h.write(t, `function main() { return 1 }`)
	req := approveRun(path)
	require.Equal(t, workflowhost.FileChanged, req.Approval.Files[0].Status)
	require.Contains(t, req.Approval.Files[0].PreviousSource, "return 2")
}

func TestServiceApprovalIsPerScript(t *testing.T) {
	h := newServiceHarness(t, false)
	src := `function main() { return 1 }`
	a := filepath.Join(h.dir, "a", "workflow.js")
	b := filepath.Join(h.dir, "b", "workflow.js")
	writeFile(t, a, src)
	writeFile(t, b, src)

	h.start(t, a, "")
	req := h.next(t, workflowhost.EventApproval)
	require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)

	h.start(t, b, "")
	req = h.next(t, workflowhost.EventApproval)
	require.Equal(t, workflowhost.FileAdded, req.Approval.Files[0].Status)
	require.NoError(t, h.svc.Respond(req.RequestID, "deny"))
	require.Equal(t, workflowhost.StatusDenied, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceRefusesMissingRequirementsBeforeRunning(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `requires({agents: ["ghost"]}); function main() { bash("touch should-not-exist") }`)
	err := h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", ScriptPath: path, SessionID: h.session})
	require.ErrorContains(t, err, `agent "ghost" not found`)
}

func TestServiceRefusesAScriptWithWarnings(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `requires(deps); function main() { bash("touch should-not-exist") }`)
	err := h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", CommandName: "x", ScriptPath: path, SessionID: h.session})
	require.ErrorContains(t, err, "requires() must be a literal")
}

func TestServiceAllowsOneRunPerSession(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { return ask("continue?") }`)
	h.start(t, path, "")
	ask := h.next(t, workflowhost.EventAsk)
	err := h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", ScriptPath: path, SessionID: h.session})
	require.ErrorIs(t, err, workflowhost.ErrRunInProgress)
	require.NoError(t, h.svc.Respond(ask.RequestID, "yes"))
	done := h.next(t, workflowhost.EventFinished)
	require.JSONEq(t, `"yes"`, string(done.Result))
}

func TestServicePublishesEachRunStateWithItsCommand(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { workflow.exclusive(() => 1); return ask("wait") }`)
	h.start(t, path, "")

	var states []workflowhost.RunState
	for len(states) < 3 {
		ev := h.next(t, workflowhost.EventState)
		require.Equal(t, "demo", ev.Command)
		require.Equal(t, h.session, ev.SessionID)
		states = append(states, ev.State)
	}
	require.Equal(t, []workflowhost.RunState{workflowhost.StateStarted, workflowhost.StateHeld, workflowhost.StateReleased}, states)

	ask := h.next(t, workflowhost.EventAsk)
	require.NoError(t, h.svc.Pause(h.session))
	paused := h.next(t, workflowhost.EventState)
	require.Equal(t, workflowhost.StatePaused, paused.State)
	require.Equal(t, "demo", paused.Command)
	require.NoError(t, h.svc.Resume(h.session))
	require.Equal(t, workflowhost.StateResumed, h.next(t, workflowhost.EventState).State)
	require.NoError(t, h.svc.Respond(ask.RequestID, "go"))
	h.next(t, workflowhost.EventFinished)
}

func TestServiceAbortEndsRun(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { ask("wait") }`)
	h.start(t, path, "")
	h.next(t, workflowhost.EventAsk)
	require.NoError(t, h.svc.Abort(h.session))
	require.Equal(t, workflowhost.StatusAborted, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceTurnRunsInSession(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "model says hi"})
	path := h.write(t, `function main() { return session.current().turn("hello") }`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
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
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"clean"`, string(done.Result))
}

func TestServiceBashRunsInEmbeddedShellWithCwd(t *testing.T) {
	h := newServiceHarness(t, true)
	sub := filepath.Join(h.dir, "sub dir")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	path := h.write(t, `function main(input) { return bash("pwd", {cwd: input.raw}).stdout.trim() }`)
	h.start(t, path, sub)
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
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
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)

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

	start := h.next(t, workflowhost.EventAgent)
	require.Equal(t, "demo", start.Command)
	require.Equal(t, h.session, start.SessionID)
	require.Equal(t, "reviewer", start.Step)
	require.Empty(t, start.Detail, "no model was pinned")
	require.Equal(t, workflowhost.StatusRunning, start.Status)
	_, err := h.fixture.Workspace.GetSession(h.fixture.Context, start.Target)
	require.NoError(t, err, "the session exists when it's published")

	end := h.next(t, workflowhost.EventAgent)
	require.Equal(t, start.Target, end.Target)
	require.Equal(t, "reviewer", end.Step)
	require.Equal(t, workflowhost.StatusCompleted, end.Status)
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)

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

	failed := h.next(t, workflowhost.EventAgent)
	require.Equal(t, "nope/nope", failed.Detail)
	require.Equal(t, workflowhost.StatusFailed, failed.Status, "an agent that never got a session never shows as running")
	require.Empty(t, failed.Target, "it has no session to open")
	require.Contains(t, failed.Message, "nope/nope")
	require.Equal(t, workflowhost.StatusFailed, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceParallelAgentsEachPublishTheirSession(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "a"}, branchfixture.Response{Text: "b"})
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() { parallel([{agent: "reviewer", prompt: "a"}, {agent: "reviewer", prompt: "b"}]) }`)
	h.start(t, path, "")

	running, ended := map[string]bool{}, map[string]bool{}
	for range 4 {
		ev := h.next(t, workflowhost.EventAgent)
		if ev.Status == workflowhost.StatusRunning {
			running[ev.Target] = true
		} else {
			ended[ev.Target] = true
		}
	}
	require.Len(t, running, 2)
	require.Equal(t, running, ended)
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceShutdownInterruptsRunsAndRefusesNewOnes(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { try { ask("wait") } catch (e) {} ; bash("echo after") }`)
	h.start(t, path, "")
	ask := h.next(t, workflowhost.EventAsk)

	ctx, cancel := context.WithTimeout(h.fixture.Context, 5*time.Second)
	defer cancel()
	require.NoError(t, h.svc.Shutdown(ctx, "agents reloaded"))

	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusInterrupted, done.Status)
	require.Equal(t, "interrupted: agents reloaded", done.Message)
	run, ok, err := h.store.GetRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, workflowhost.StatusInterrupted, run.Status)
	require.Error(t, h.svc.Respond(ask.RequestID, "late"))
	err = h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", ScriptPath: path, SessionID: h.session})
	require.ErrorIs(t, err, workflowhost.ErrShuttingDown)
}

func TestServiceShutdownInterruptsAPendingApproval(t *testing.T) {
	h := newServiceHarness(t, false)
	path := h.write(t, `function main() { return 1 }`)
	h.start(t, path, "")
	h.next(t, workflowhost.EventApproval)

	ctx, cancel := context.WithTimeout(h.fixture.Context, 5*time.Second)
	defer cancel()
	require.NoError(t, h.svc.Shutdown(ctx, "agents reloaded"))

	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusInterrupted, done.Status)
	require.Equal(t, "interrupted: agents reloaded", done.Message)
}

func TestServiceShutdownClosesSubscriptions(t *testing.T) {
	h := newServiceHarness(t, true)
	ctx, cancel := context.WithTimeout(h.fixture.Context, 5*time.Second)
	defer cancel()
	require.NoError(t, h.svc.Shutdown(ctx, "test"))
	select {
	case _, ok := <-h.events:
		require.False(t, ok, "no events after shutdown")
	case <-ctx.Done():
		t.Fatal("subscription still open after shutdown")
	}
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
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"sleep 30"`, string(done.Result))
	for _, job := range mgr.ListBySession(h.session) {
		require.True(t, job.Done, "job %s still running", job.ID)
	}
}

func TestServiceRepublishesUnansweredRequestsUntilAnswered(t *testing.T) {
	h := newServiceHarness(t, true, func(d *workflowhost.Deps) { d.RepublishInterval = 20 * time.Millisecond })
	path := h.write(t, `function main() { return ask("still there?") }`)
	h.start(t, path, "")
	first := h.next(t, workflowhost.EventAsk)
	again := h.next(t, workflowhost.EventAsk)
	require.Equal(t, first.RequestID, again.RequestID)
	require.NoError(t, h.svc.Respond(first.RequestID, "yes"))
	done := h.next(t, workflowhost.EventFinished)
	require.JSONEq(t, `"yes"`, string(done.Result))
	select {
	case ev := <-h.events:
		require.NotEqual(t, workflowhost.EventAsk, ev.Payload.Kind, "answered request was republished")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServiceRefusesAWorkflowFromASessionAnotherRunUses(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { const s = session.new({}); workflow.focus(s); return ask("hold on") }`)
	h.start(t, path, "")
	focus := h.next(t, workflowhost.EventFocus)
	ask := h.next(t, workflowhost.EventAsk)
	err := h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", ScriptPath: path, SessionID: focus.Target})
	require.ErrorIs(t, err, workflowhost.ErrRunInProgress)
	require.NoError(t, h.svc.Respond(ask.RequestID, "ok"))
	h.next(t, workflowhost.EventFinished)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestServiceFreesTheSessionBeforeReportingFinished(t *testing.T) {
	h := newServiceHarness(t, true)
	// A subscriber that never reads makes each must-deliver publish block
	// for its timeout once the buffer is full, which widens the window
	// between reporting "finished" and freeing the session.
	_ = h.svc.Subscribe(h.fixture.Context)
	path := h.write(t, `function main() { for (let i = 0; i < 66; i++) status("s", "" + i) }`)
	for range 6 {
		h.start(t, path, "")
		h.next(t, workflowhost.EventFinished)
		// Starting again the moment "finished" arrives must work.
		require.NoError(t, h.svc.Start(h.fixture.Context, workflowhost.Invocation{
			CommandID: "project:demo", CommandName: "demo", Source: "project",
			ScriptPath: path, SessionID: h.session,
		}))
		h.next(t, workflowhost.EventFinished)
	}
}

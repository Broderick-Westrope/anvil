package workflowhost_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
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
		Messages:    f.Messages,
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

	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"hello"`, string(done.Result))
	view, _, err := h.svc.RunView(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	step, detail := view.Current()
	require.Equal(t, []string{"echo", "hello"}, []string{step, detail})

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
	writeFile(t, filepath.Join(h.dir, "z-last.md"), "sorted after the script")
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
	require.Equal(t, []string{"workflow.js", "a-first.md", filepath.Join("references", "guide.md"), "z-last.md"}, paths,
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
	// Only the first 8 KiB are searched for a NUL byte.
	leadingNUL := "\x00rest"
	lastSniffedNUL := strings.Repeat("a", 8*1024-1) + "\x00"
	unsniffedNUL := strings.Repeat("a", 8*1024) + "\x00"
	writeFile(t, filepath.Join(h.dir, "leading.bin"), leadingNUL)
	writeFile(t, filepath.Join(h.dir, "last-sniffed.bin"), lastSniffedNUL)
	writeFile(t, filepath.Join(h.dir, "unsniffed.txt"), unsniffedNUL)
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
	require.Equal(t, "binary", got["leading.bin"].Omitted)
	require.Equal(t, "binary", got["last-sniffed.bin"].Omitted)
	require.Equal(t, unsniffedNUL, got["unsniffed.txt"].Source)
	require.Empty(t, got["unsniffed.txt"].Omitted)
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

	started := h.next(t, workflowhost.EventState)
	require.Equal(t, workflowhost.StateStarted, started.State)
	require.Equal(t, "demo", started.Command)
	require.Equal(t, h.session, started.SessionID)

	ask := h.next(t, workflowhost.EventAsk)
	require.NoError(t, h.svc.Pause(ask.RunID))
	paused := h.next(t, workflowhost.EventState)
	require.Equal(t, workflowhost.StatePaused, paused.State)
	require.Equal(t, "demo", paused.Command)
	view, _, err := h.svc.RunView(h.fixture.Context, ask.RunID)
	require.NoError(t, err)
	require.True(t, view.Paused, "pause is recorded, so a reloaded view shows it")
	require.NoError(t, h.svc.Resume(ask.RunID))
	require.Equal(t, workflowhost.StateResumed, h.next(t, workflowhost.EventState).State)
	view, _, err = h.svc.RunView(h.fixture.Context, ask.RunID)
	require.NoError(t, err)
	require.False(t, view.Paused)
	require.NoError(t, h.svc.Respond(ask.RequestID, "go"))
	h.next(t, workflowhost.EventFinished)
}

func TestServiceAbortEndsRun(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { ask("wait") }`)
	h.start(t, path, "")
	ask := h.next(t, workflowhost.EventAsk)
	require.ErrorIs(t, h.svc.Abort("another-run"), workflowhost.ErrNoRun)
	require.NoError(t, h.svc.Abort(ask.RunID))
	require.Equal(t, workflowhost.StatusAborted, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceTurnRunsInSession(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "model says hi"})
	path := h.write(t, `function main() { return session.new().turn("hello") }`)
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
	path := h.write(t, `function main() { const s = session.new(); s.turn("do the thing"); return s.id }`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)

	msgs, err := h.fixture.Workspace.ListUserMessages(h.fixture.Context, resultString(t, done))
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	command, prompt, ok := commands.ParseWorkflowTurnXML(msgs[0].Content().Text)
	require.True(t, ok, msgs[0].Content().Text)
	require.Equal(t, "/demo", command)
	require.Equal(t, "do the thing", prompt)
}

// agentItems returns the subagents a finished run's view shows.
func agentItems(t *testing.T, h *serviceHarness, runID string) []workflowhost.RunItem {
	t.Helper()
	view, ok, err := h.svc.RunView(h.fixture.Context, runID)
	require.NoError(t, err)
	require.True(t, ok)
	var items []workflowhost.RunItem
	for _, p := range view.Phases {
		for _, item := range p.Items {
			if item.Kind == workflowhost.RunItemAgent {
				items = append(items, item)
			}
		}
	}
	return items
}

func TestServiceAgentRecordsItsSession(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "looks fine"})
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() { return agent("reviewer", "review") }`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)

	items := agentItems(t, h, done.RunID)
	require.Len(t, items, 1)
	require.Equal(t, "reviewer", items[0].Agent)
	require.Empty(t, items[0].Model, "no model was pinned")
	require.Equal(t, workflowhost.StatusCompleted, items[0].Status)
	child, err := h.fixture.Workspace.GetSession(h.fixture.Context, items[0].SessionID)
	require.NoError(t, err)
	require.Equal(t, h.session, child.ParentSessionID)
	require.Equal(t, "/demo: reviewer", child.Title)
}

func TestServiceAgentRecordsFailure(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() { return agent("reviewer", "review", {model: "nope/nope"}) }`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusFailed, done.Status)

	items := agentItems(t, h, done.RunID)
	require.Len(t, items, 1)
	require.Equal(t, "nope/nope", items[0].Model)
	require.Equal(t, workflowhost.StatusFailed, items[0].Status)
	require.Empty(t, items[0].SessionID, "it has no session to open")
	require.Contains(t, items[0].Err, "nope/nope")
}

func TestServiceParallelAgentsEachRecordTheirSession(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "a"}, branchfixture.Response{Text: "b"})
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() { parallel([{agent: "reviewer", prompt: "a"}, {agent: "reviewer", prompt: "b"}]) }`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)

	items := agentItems(t, h, done.RunID)
	require.Len(t, items, 2)
	require.NotEmpty(t, items[0].SessionID)
	require.NotEqual(t, items[0].SessionID, items[1].SessionID)
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
	// The script makes a session and waits, so the test can give that
	// session a background job before the script holds it.
	path := h.write(t, `function main() {
  const s = session.new()
  ask(s.id)
  let refused
  try { workflow.exclusive(() => 1) } catch (e) { refused = e instanceof JobsRunning && e.jobs[0].command }
  workflow.exclusive(() => 1, {stopJobs: true})
  return refused
}`)
	h.start(t, path, "")
	ask := h.next(t, workflowhost.EventAsk)
	owned := ask.Question

	mgr := shell.GetBackgroundShellManager()
	bs, err := mgr.Start(h.fixture.Context, h.dir, nil, "sleep 30", "watcher")
	require.NoError(t, err)
	jobID, err := mgr.Publish(h.fixture.Context, bs.ID(), shell.PublishOptions{SessionID: owned, Origin: shell.OriginExplicit})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Kill(jobID) })
	require.NoError(t, h.svc.Respond(ask.RequestID, "go"))

	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"sleep 30"`, string(done.Result))
	for _, job := range mgr.ListBySession(owned) {
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
	path := h.write(t, `function main() { const s = session.new({}); return ask(s.id) }`)
	h.start(t, path, "")
	ask := h.next(t, workflowhost.EventAsk)
	err := h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", ScriptPath: path, SessionID: ask.Question})
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

func TestServiceKeepsAScriptChosenSessionTitle(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "ok"}, branchfixture.Response{Text: "Generated Title"})
	path := h.write(t, `function main() {
  const s = session.new({ title: "/demo second" })
  s.turn("hello")
  return s.id
}`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	var id string
	require.NoError(t, json.Unmarshal(done.Result, &id))
	h.fixture.WaitBackgroundJobs()
	s, err := h.fixture.Sessions.Get(h.fixture.Context, id)
	require.NoError(t, err)
	require.Equal(t, "/demo second", s.Title)
	require.True(t, s.TitleIsCustom)
}

func TestServiceApprovalListsFilesRemovedSinceTheLastApproval(t *testing.T) {
	h := newServiceHarness(t, false)
	for _, name := range []string{"a-gone.md", "b-kept.md", "c-gone.md"} {
		writeFile(t, filepath.Join(h.dir, name), name)
	}
	path := h.write(t, `function main() { return 1 }`)
	h.start(t, path, "")
	req := h.next(t, workflowhost.EventApproval)
	require.NoError(t, h.svc.Respond(req.RequestID, "approve"))
	require.Equal(t, workflowhost.StatusCompleted, h.next(t, workflowhost.EventFinished).Status)

	require.NoError(t, os.Remove(filepath.Join(h.dir, "a-gone.md")))
	require.NoError(t, os.Remove(filepath.Join(h.dir, "c-gone.md")))
	h.start(t, path, "")
	req = h.next(t, workflowhost.EventApproval)
	var got []string
	for _, f := range req.Approval.Files {
		got = append(got, f.Path+" "+string(f.Status))
	}
	require.Equal(t, []string{
		"workflow.js unchanged", "a-gone.md removed", "b-kept.md unchanged", "c-gone.md removed",
	}, got, "removed files sort among the current ones")
	require.NoError(t, h.svc.Respond(req.RequestID, "deny"))
	require.Equal(t, workflowhost.StatusDenied, h.next(t, workflowhost.EventFinished).Status)
}

func TestServiceApprovalNamesTheCommandOrigin(t *testing.T) {
	tests := map[string]struct {
		source string
		want   string
	}{
		"personal command": {source: "", want: "personal"},
		"project command":  {source: "project", want: "project"},
		"plugin command":   {source: "plugin:tools", want: "plugin:tools"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := newServiceHarness(t, false)
			path := h.write(t, `function main() { return 1 }`)
			require.NoError(t, h.svc.Start(h.fixture.Context, workflowhost.Invocation{
				CommandID: "demo", CommandName: "demo", Source: tc.source, ScriptPath: path, SessionID: h.session,
			}))
			req := h.next(t, workflowhost.EventApproval)
			require.Equal(t, tc.want, req.Approval.Origin)
			require.NoError(t, h.svc.Respond(req.RequestID, "deny"))
			h.next(t, workflowhost.EventFinished)
		})
	}
}

func TestServiceRepublishesEveryTwoSecondsByDefault(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { return ask("still there?") }`)
	h.start(t, path, "")
	first := h.next(t, workflowhost.EventAsk)
	published := time.Now()
	again := h.next(t, workflowhost.EventAsk)
	require.Equal(t, first.RequestID, again.RequestID)
	require.GreaterOrEqual(t, time.Since(published), 1500*time.Millisecond)
	require.NoError(t, h.svc.Respond(first.RequestID, "yes"))
	h.next(t, workflowhost.EventFinished)
}

func TestServiceSkillsComeFromTheCoordinator(t *testing.T) {
	h := newServiceHarness(t, true)
	jq := h.fixture.Coordinator.ActiveSkillByName("jq")
	require.NotNil(t, jq, "the builtin jq skill is active")
	path := h.write(t, `requires({skills: ["jq"]})
function main() {
  let missing
  try { skill("ghost") } catch (e) { missing = e.message }
  return {jq: skill("jq"), missing}
}`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	var got struct {
		JQ      struct{ Body, Location string }
		Missing string
	}
	require.NoError(t, json.Unmarshal(done.Result, &got))
	require.Equal(t, jq.Instructions, got.JQ.Body)
	require.Equal(t, jq.SkillFilePath, got.JQ.Location)
	require.Equal(t, `skill "ghost" not found`, got.Missing)
}

func TestServiceRefusesAScriptRequiringAMissingSkill(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `requires({skills: ["ghost"]}); function main() { bash("touch should-not-exist") }`)
	err := h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", ScriptPath: path, SessionID: h.session})
	require.ErrorContains(t, err, `skill "ghost" not found`)
}

func TestServiceTurnPreloadsItsSkills(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "ok"})
	path := h.write(t, `requires({skills: ["jq"]})
function main() { const s = session.new(); s.turn("hello", {skills: ["jq"]}); return s.id }`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)

	msgs, err := h.fixture.Workspace.ListUserMessages(h.fixture.Context, resultString(t, done))
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	_, prompt, ok := commands.ParseWorkflowTurnXML(msgs[0].Content().Text)
	require.True(t, ok, msgs[0].Content().Text)
	jq := h.fixture.Coordinator.ActiveSkillByName("jq")
	require.Equal(t, skills.FormatContentXML("jq", jq.Instructions)+"\n\nhello", prompt)
}

func TestServiceAgentWithoutStructuredOutputThrowsSchemaError(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "no result"}, branchfixture.Response{Text: "still no result"})
	path := h.write(t, `requires({agents: ["reviewer"]})
const S = {type: "object", properties: {kind: {type: "string"}}, required: ["kind"]}
function main() {
  try { agent("reviewer", "review", {schema: S}) } catch (e) { return [e.name, e.raw] }
}`)
	h.start(t, path, "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `["SchemaError", "still no result"]`, string(done.Result))
}

func TestServiceBashReturnsFailuresAsResults(t *testing.T) {
	h := newServiceHarness(t, true)
	// A script with no shebang runs in-process, so its parse error stops
	// the command partway rather than exiting with a status.
	broken := filepath.Join(t.TempDir(), "broken.sh")
	writeFile(t, broken, "echo 'unterminated")
	path := h.write(t, `function main(input) {
  return {
    failed: bash("echo out; echo err >&2; exit 3", {check: false}),
    unparsable: bash("echo 'unterminated", {check: false}),
    halted: bash("echo before >&2; '" + input.raw + "'", {check: false}),
  }
}`)
	h.start(t, path, broken)
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	var got struct {
		Failed, Unparsable, Halted struct {
			Stdout, Stderr string
			ExitCode       int
		}
	}
	require.NoError(t, json.Unmarshal(done.Result, &got))
	require.Equal(t, "out\n", got.Failed.Stdout)
	require.Equal(t, "err\n", got.Failed.Stderr)
	require.Equal(t, 3, got.Failed.ExitCode)
	require.Empty(t, got.Unparsable.Stdout)
	require.Contains(t, got.Unparsable.Stderr, "reached EOF without closing quote")
	require.Equal(t, 1, got.Unparsable.ExitCode)
	require.True(t, strings.HasPrefix(got.Halted.Stderr, "before\n"), got.Halted.Stderr)
	require.Contains(t, got.Halted.Stderr, "could not parse")
}

func TestServiceRecordsAnInterruptedBashAsFailed(t *testing.T) {
	h := newServiceHarness(t, true)
	path := h.write(t, `function main() { status("go"); return bash("sleep 30", {check: false}) }`)
	h.start(t, path, "")
	started := h.next(t, workflowhost.EventState)
	require.Eventually(t, func() bool {
		events, err := h.store.ListEvents(h.fixture.Context, started.RunID)
		return err == nil && slices.ContainsFunc(events, func(ev workflow.Event) bool {
			return ev.Primitive == "bash" && ev.Phase == workflow.PhaseStarted
		})
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, h.svc.Abort(started.RunID))
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusAborted, done.Status)

	events, err := h.store.ListEvents(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	var phases []workflow.Phase
	for _, ev := range events {
		if ev.Primitive == "bash" {
			phases = append(phases, ev.Phase)
		}
	}
	require.Equal(t, []workflow.Phase{workflow.PhaseStarted, workflow.PhaseFailed}, phases)
}

func TestServiceRecordsTheCanonicalWorkingDir(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, link))
	tests := map[string]struct {
		dir, want string
	}{
		"relative": {dir: ".", want: cwd},
		"symlink":  {dir: link, want: target},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			want, err := filepath.EvalSymlinks(tc.want)
			require.NoError(t, err)
			h := newServiceHarness(t, true, func(d *workflowhost.Deps) { d.WorkingDir = tc.dir })
			path := h.write(t, `function main() { return 1 }`)
			h.start(t, path, "")
			done := h.next(t, workflowhost.EventFinished)
			require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
			run, ok, err := h.store.GetRun(h.fixture.Context, done.RunID)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, want, run.WorkingDir)
		})
	}
}

func TestServiceShutdownBetweenApprovalAndStartInterruptsTheRun(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := newServiceHarness(t, true, func(d *workflowhost.Deps) {
		d.SkipApproval = func() bool {
			close(entered)
			<-release
			return true
		}
	})
	path := h.write(t, `function main() { bash("touch should-not-exist") }`)
	h.start(t, path, "")
	<-entered

	shutdown := make(chan error, 1)
	go func() { shutdown <- h.svc.Shutdown(context.WithoutCancel(h.fixture.Context), "agents reloaded") }()
	require.Eventually(t, func() bool {
		err := h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", ScriptPath: path, SessionID: h.session})
		return errors.Is(err, workflowhost.ErrShuttingDown)
	}, 5*time.Second, time.Millisecond)
	close(release)

	var done workflowhost.Event
	for done.Kind != workflowhost.EventFinished {
		select {
		case ev := <-h.events:
			done = ev.Payload
			require.NotEqual(t, workflowhost.EventState, done.Kind, "the run never starts")
		case <-time.After(10 * time.Second):
			t.Fatal("no finished event")
		}
	}
	require.Equal(t, workflowhost.StatusInterrupted, done.Status, done.Message)
	require.Equal(t, "interrupted: agents reloaded", done.Message)
	require.NoError(t, <-shutdown)
	run, ok, err := h.store.GetRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, workflowhost.StatusInterrupted, run.Status)
	require.NoFileExists(t, filepath.Join(h.fixture.Config.WorkingDir(), "should-not-exist"))
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A run whose outcome can't be recorded still finishes, and the failure is
// logged. It replaces the default logger, so it doesn't run in parallel.
func TestServiceLogsAnOutcomeItCantRecord(t *testing.T) {
	tests := map[string]struct {
		// stopBeforeStart shuts the service down after approval but before
		// the run starts.
		stopBeforeStart bool
		wantStatus      workflowhost.Status
	}{
		"after the run ends":           {wantStatus: workflowhost.StatusCompleted},
		"for a run that never started": {stopBeforeStart: true, wantStatus: workflowhost.StatusInterrupted},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			logs := &lockedBuffer{}
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			entered, release := make(chan struct{}), make(chan struct{})
			h := newServiceHarness(t, true, func(d *workflowhost.Deps) {
				d.SkipApproval = func() bool {
					close(entered)
					<-release
					return true
				}
			})
			_, err := h.fixture.Conn.ExecContext(h.fixture.Context, `CREATE TRIGGER refuse_finish BEFORE UPDATE OF status ON workflow_runs
BEGIN SELECT RAISE(ABORT, 'finish refused'); END`)
			require.NoError(t, err)
			h.start(t, h.write(t, `function main() { return 1 }`), "")
			<-entered
			shutdown := make(chan error, 1)
			if tc.stopBeforeStart {
				go func() { shutdown <- h.svc.Shutdown(context.WithoutCancel(h.fixture.Context), "agents reloaded") }()
				require.Eventually(t, func() bool {
					err := h.svc.Start(h.fixture.Context, workflowhost.Invocation{CommandID: "x", ScriptPath: filepath.Join(h.dir, "workflow.js"), SessionID: h.session})
					return errors.Is(err, workflowhost.ErrShuttingDown)
				}, 5*time.Second, time.Millisecond)
			}
			close(release)

			done := h.next(t, workflowhost.EventFinished)
			require.Equal(t, tc.wantStatus, done.Status, done.Message)
			require.Contains(t, logs.String(), "Failed to record workflow run outcome")
			require.Contains(t, logs.String(), "finish refused")
			if tc.stopBeforeStart {
				require.NoError(t, <-shutdown)
			}
		})
	}
}

func TestServiceMarksTheRunInItsSessionAndRebuildsItsView(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "solo"}, branchfixture.Response{Text: "a"}, branchfixture.Response{Text: "b"})
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() {
  const builder = session.new({title: "builder"})
  status("review", "pass 1/3")
  agent("reviewer", "solo")
  parallel([{agent: "reviewer", prompt: "a"}, {agent: "reviewer", prompt: "b"}])
  status("hand over")
  return ask("ship it?", {options: ["yes", "no"]})
}`)
	h.start(t, path, "track downloads")
	ask := h.next(t, workflowhost.EventAsk)
	require.NoError(t, h.svc.Respond(ask.RequestID, "yes"))
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status)

	view, ok, err := h.svc.RunView(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.True(t, ok)

	require.Equal(t, "demo", view.Command, "the view names the command the run was started as")
	invoker, err := h.fixture.Sessions.Get(h.fixture.Context, h.session)
	require.NoError(t, err)
	marker, err := h.fixture.Messages.Get(h.fixture.Context, invoker.LeafMessageID)
	require.NoError(t, err)
	require.Equal(t, message.MessageTypeWorkflowRun, marker.MessageType)
	require.Equal(t, []message.ContentPart{message.WorkflowRunContent{RunID: done.RunID, Command: "demo", Input: "track downloads"}}, marker.Parts)

	require.Equal(t, workflowhost.StatusCompleted, view.Status)
	require.Equal(t, "track downloads", view.Input)
	step, detail := view.Current()
	require.Equal(t, [2]string{"hand over", ""}, [2]string{step, detail})
	require.Len(t, view.Phases, 3)

	builder := view.Phases[0].Items
	require.Len(t, builder, 1)
	require.Equal(t, workflowhost.RunItemSession, builder[0].Kind)
	require.Equal(t, "builder", builder[0].Title)
	require.NotEmpty(t, builder[0].SessionID)

	// The run owns its builder: it's a child of the invoking session that
	// the user can still talk to, and it stays out of the session list.
	owned, err := h.fixture.Sessions.Get(h.fixture.Context, builder[0].SessionID)
	require.NoError(t, err)
	require.Equal(t, h.session, owned.ParentSessionID)
	require.Equal(t, session.WorkflowKindSession, owned.WorkflowKind)
	require.True(t, owned.Conversational())
	listed, err := h.fixture.Sessions.List(h.fixture.Context, "")
	require.NoError(t, err)
	for _, s := range listed {
		require.NotEqual(t, owned.ID, s.ID, "a run's sessions stay out of the session list")
	}

	review := view.Phases[1]
	require.Equal(t, "review", review.Step)
	require.Equal(t, "pass 1/3", review.Detail)
	require.Len(t, review.Items, 3)
	sessions := map[string]bool{}
	for _, item := range review.Items {
		agentSession, err := h.fixture.Sessions.Get(h.fixture.Context, item.SessionID)
		require.NoError(t, err)
		require.Equal(t, session.WorkflowKindAgent, agentSession.WorkflowKind)
		require.False(t, agentSession.Conversational())
		require.Equal(t, workflowhost.RunItemAgent, item.Kind)
		require.Equal(t, "reviewer", item.Agent)
		require.Equal(t, workflowhost.StatusCompleted, item.Status)
		require.NotEmpty(t, item.SessionID)
		sessions[item.SessionID] = true
	}
	require.Len(t, sessions, 3, "each subagent opens its own session")

	answer := view.Phases[2].Items
	require.Equal(t, []workflowhost.RunItem{{Kind: workflowhost.RunItemAsk, Question: "ship it?", Answer: "yes", Status: workflowhost.StatusCompleted}}, answer)
}

// busyInvoker starts a turn in the invoking session that runs until the
// returned release is called.
func (h *serviceHarness) busyInvoker(t *testing.T) (release func(), done <-chan error) {
	t.Helper()
	require.NoError(t, h.fixture.Sessions.Rename(h.fixture.Context, h.session, "invoker", true))
	started, unblock := make(chan struct{}), make(chan struct{})
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "still talking", Started: started, Release: unblock})
	finished := make(chan error, 1)
	go func() {
		_, err := h.fixture.Coordinator.Run(h.fixture.Context, h.session, "hello")
		finished <- err
	}()
	select {
	case <-started:
	case <-h.fixture.Context.Done():
		t.Fatal(h.fixture.Context.Err())
	}
	return sync.OnceFunc(func() { close(unblock) }), finished
}

func TestServiceMarksTheRunAfterTheInvokersTurnInFlight(t *testing.T) {
	h := newServiceHarness(t, true)
	release, turnDone := h.busyInvoker(t)
	h.start(t, h.write(t, `function main() { return 1 }`), "")
	release()
	require.NoError(t, <-turnDone)
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status)

	run, _, err := h.store.GetRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	marker, err := h.fixture.Messages.Get(h.fixture.Context, run.MarkerMessageID)
	require.NoError(t, err)
	reply, err := h.fixture.Messages.Get(h.fixture.Context, marker.ParentMessageID)
	require.NoError(t, err)
	require.Equal(t, message.Assistant, reply.Role, "the marker follows the turn that was running, on the same branch")
	require.Equal(t, "still talking", reply.Content().Text)
	invoker, err := h.fixture.Sessions.Get(h.fixture.Context, h.session)
	require.NoError(t, err)
	require.Equal(t, marker.ID, invoker.LeafMessageID)
}

func TestServiceAbortEndsARunStillWaitingToBeMarked(t *testing.T) {
	h := newServiceHarness(t, true)
	release, turnDone := h.busyInvoker(t)
	t.Cleanup(release)
	h.start(t, h.write(t, `function main() { return 1 }`), "")
	started := h.next(t, workflowhost.EventState)
	require.Equal(t, workflowhost.StateStarted, started.State)
	require.NoError(t, h.svc.Abort(started.RunID))

	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusAborted, done.Status)
	run, ok, err := h.store.GetRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, run.MarkerMessageID)
	require.Equal(t, workflowhost.StatusAborted, run.Status)
	require.Empty(t, h.fixture.Coordinator.QueuedPromptsList(h.session))

	release()
	require.NoError(t, <-turnDone)
	invoker, err := h.fixture.Sessions.Get(h.fixture.Context, h.session)
	require.NoError(t, err)
	leaf, err := h.fixture.Messages.Get(h.fixture.Context, invoker.LeafMessageID)
	require.NoError(t, err)
	require.NotEqual(t, message.MessageTypeWorkflowRun, leaf.MessageType)
}

// ownerlessSessions can't record which run created a session.
type ownerlessSessions struct{ session.Service }

func (ownerlessSessions) SetWorkflowOwner(context.Context, string, session.WorkflowOwner) error {
	return errors.New("database is locked")
}

func TestServiceStopsASubagentItCantTieToTheRun(t *testing.T) {
	h := newServiceHarness(t, true, func(d *workflowhost.Deps) { d.Sessions = ownerlessSessions{d.Sessions} })
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "should never be asked"})
	h.start(t, h.write(t, `requires({agents: ["reviewer"]})
function main() { try { agent("reviewer", "look") } catch (e) { return e.message } }`), "")

	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status)
	require.JSONEq(t, `"record subagent session: database is locked"`, string(done.Result))
}

func TestServiceRunViewEndsOpenCallsTheWayTheRunEnded(t *testing.T) {
	h := newServiceHarness(t, true)
	ctx := h.fixture.Context
	require.NoError(t, h.store.StartRun(ctx, workflowhost.RunRecord{ID: "run-dead", Command: "project:demo", InvokingSessionID: h.session}))
	rec := h.store.Recorder("run-dead")
	require.NoError(t, rec.Record(ctx, workflow.Event{Seq: 1, Primitive: "agent", Phase: workflow.PhaseStarted, Args: json.RawMessage(`{"agent":"reviewer"}`), At: time.Now()}))
	require.NoError(t, rec.Record(ctx, workflow.Event{Seq: 2, Primitive: "ask", Phase: workflow.PhaseStarted, Args: json.RawMessage(`{"question":"q"}`), At: time.Now()}))
	// The process died mid-call; startup marks the run interrupted.
	require.NoError(t, h.store.FinishRun(ctx, "run-dead", workflowhost.StatusInterrupted, nil, "interrupted: anvil exited"))

	view, ok, err := h.svc.RunView(ctx, "run-dead")
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, view.WaitingForUser())
	items := view.Phases[0].Items
	require.Len(t, items, 2)
	for _, item := range items {
		require.Equal(t, workflowhost.StatusInterrupted, item.Status, item.Kind)
	}
}

// resultString decodes a run's JSON string result.
func resultString(t *testing.T, done workflowhost.Event) string {
	t.Helper()
	var s string
	require.NoError(t, json.Unmarshal(done.Result, &s))
	return s
}

func TestServiceForkCopiesTheConversationBeforeTheRun(t *testing.T) {
	h := newServiceHarness(t, true)
	ctx := h.fixture.Context
	before, err := h.fixture.Messages.Create(ctx, h.session, message.CreateMessageParams{
		Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "we agreed: use customer-frontend"}},
	})
	require.NoError(t, err)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "building"}, branchfixture.Response{Text: "still building"})

	path := h.write(t, `function main() {
  const builder = session.current().fork({title: "/demo approach 1"})
  ask("chat in your session now")
  builder.turn("build it")
  const again = builder.fork({title: "/demo approach 2"})
  return [builder.id, again.id]
}`)
	h.start(t, path, "")
	ask := h.next(t, workflowhost.EventAsk)
	// The user carries on in their own session while the run works.
	_, err = h.fixture.Messages.Create(ctx, h.session, message.CreateMessageParams{
		Role: message.User, ParentMessageID: leafOf(t, h, h.session), Parts: []message.ContentPart{message.TextContent{Text: "said after the run started"}},
	})
	require.NoError(t, err)
	require.NoError(t, h.svc.Respond(ask.RequestID, "ok"))
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)

	var ids []string
	require.NoError(t, json.Unmarshal(done.Result, &ids))
	texts := func(sessionID string) []string {
		path, err := h.fixture.Messages.GetBranchPath(ctx, leafOf(t, h, sessionID))
		require.NoError(t, err)
		var out []string
		for _, m := range path {
			if m.Role == message.User {
				out = append(out, m.Content().Text)
			}
		}
		return out
	}
	first := texts(ids[0])
	require.Equal(t, "we agreed: use customer-frontend", first[0])
	require.Len(t, first, 2, "the fork has what came before the run, then its own turn")
	require.NotContains(t, strings.Join(first, "\n"), "said after the run started")

	second := texts(ids[1])
	require.Equal(t, first, second, "forking a run's session copies it as it is")

	for _, id := range ids {
		s, err := h.fixture.Sessions.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, h.session, s.ParentSessionID)
		require.Equal(t, session.WorkflowKindSession, s.WorkflowKind)
	}
	view, _, err := h.svc.RunView(ctx, done.RunID)
	require.NoError(t, err)
	var shown []string
	for _, item := range view.Phases[0].Items {
		if item.Kind == workflowhost.RunItemSession {
			shown = append(shown, item.SessionID)
		}
	}
	require.Equal(t, ids, shown, "the run view lists both forks")

	firstSession, err := h.fixture.Sessions.Get(ctx, ids[0])
	require.NoError(t, err)
	require.Equal(t, "/demo approach 1", firstSession.Title)
	require.Equal(t, before.ID, firstSession.ForkedFrom, "the fork records the source message its copy ends at")
	secondSession, err := h.fixture.Sessions.Get(ctx, ids[1])
	require.NoError(t, err)
	builderPath, err := h.fixture.Messages.GetBranchPath(ctx, leafOf(t, h, ids[0]))
	require.NoError(t, err)
	require.Equal(t, builderPath[len(builderPath)-1].ID, secondSession.ForkedFrom)
	require.NotEqual(t, before.ID, firstSession.LeafMessageID)
}

func leafOf(t *testing.T, h *serviceHarness, sessionID string) string {
	t.Helper()
	s, err := h.fixture.Sessions.Get(h.fixture.Context, sessionID)
	require.NoError(t, err)
	return s.LeafMessageID
}

func TestServiceAnnouncesTheInvokingSessionsNewLeaf(t *testing.T) {
	h := newServiceHarness(t, true)
	updates := h.fixture.Sessions.Subscribe(h.fixture.Context)
	h.start(t, h.write(t, `function main() { return 1 }`), "")
	done := h.next(t, workflowhost.EventFinished)
	run, _, err := h.store.GetRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)

	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-updates:
			if ev.Payload.ID == h.session && ev.Payload.LeafMessageID == run.MarkerMessageID {
				return
			}
		case <-timeout:
			t.Fatal("the invoking session's move to the marker was never announced")
		}
	}
}

// Metadata the UI writes (an MCP toggle, a model change), written beside a
// turn in flight, would branch off and take the session's leaf away from
// the turn's reply, and from a workflow's marker; so it waits its turn and
// goes on the current leaf.
func TestMetadataWaitsForTheTurnInFlightAndFollowsIt(t *testing.T) {
	h := newServiceHarness(t, true)
	ctx := h.fixture.Context
	release, turnDone := h.busyInvoker(t)

	written := make(chan error, 1)
	go func() {
		written <- h.fixture.Workspace.WriteMetadataEntry(ctx, h.session, message.CreateMessageParams{
			Role: message.System, MessageType: message.MessageTypeMCPToggle,
			Parts: []message.ContentPart{message.MCPToggleContent{ServerName: "s", Enabled: true}},
		})
	}()
	select {
	case err := <-written:
		t.Fatalf("metadata was written while a turn was in flight (err %v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	require.NoError(t, <-turnDone)
	require.NoError(t, <-written)

	leaf, err := h.fixture.Messages.Get(ctx, leafOf(t, h, h.session))
	require.NoError(t, err)
	require.Equal(t, message.MessageTypeMCPToggle, leaf.MessageType)
	reply, err := h.fixture.Messages.Get(ctx, leaf.ParentMessageID)
	require.NoError(t, err)
	require.Equal(t, "still talking", reply.Content().Text, "the metadata follows the turn's reply on the same branch")
}

// brokenCopies can't copy a conversation.
type brokenCopies struct{ message.Service }

func (brokenCopies) CopyBranch(context.Context, string, string) (string, string, error) {
	return "", "", errors.New("disk full")
}

func TestServiceRemovesAForkItCouldntFill(t *testing.T) {
	h := newServiceHarness(t, true, func(d *workflowhost.Deps) { d.Messages = brokenCopies{d.Messages} })
	_, err := h.fixture.Messages.Create(h.fixture.Context, h.session, message.CreateMessageParams{
		Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	})
	require.NoError(t, err)
	h.start(t, h.write(t, `function main() { try { session.current().fork() } catch (e) { return e.message } }`), "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.Contains(t, string(done.Result), "disk full")

	sessions, err := h.fixture.Sessions.ListByWorkflowRun(h.fixture.Context, done.RunID)
	require.NoError(t, err)
	require.Empty(t, sessions, "no half-made fork is left behind")
}

func TestDeletingTheInvokingSessionDeletesWhatItsRunsMade(t *testing.T) {
	h := newServiceHarness(t, true)
	ctx := h.fixture.Context
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "sub"})
	_, err := h.fixture.Messages.Create(ctx, h.session, message.CreateMessageParams{
		Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "private"}},
	})
	require.NoError(t, err)
	h.start(t, h.write(t, `requires({agents: ["reviewer"]})
function main() { session.current().fork(); agent("reviewer", "look") }`), "")
	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	owned, err := h.fixture.Sessions.ListByWorkflowRun(ctx, done.RunID)
	require.NoError(t, err)
	require.Len(t, owned, 2)

	require.NoError(t, h.fixture.Sessions.Delete(ctx, h.session))
	for _, s := range owned {
		_, err := h.fixture.Sessions.Get(ctx, s.ID)
		require.Error(t, err, "%s outlived the session it was made from", s.Title)
	}
}

// The UI reloads a run's view when it hears a change, so each change must
// be announced only once its event is in the log.
func TestServiceAnnouncesAChangeOnlyOnceItsRecorded(t *testing.T) {
	h := newServiceHarness(t, true)
	h.start(t, h.write(t, `function main() { status("a"); status("b"); return now() }`), "")
	announced := 0
	for {
		ev := <-h.events
		switch ev.Payload.Kind {
		case workflowhost.EventChanged:
			announced++
			recorded, err := h.store.ListEvents(h.fixture.Context, ev.Payload.RunID)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(recorded), announced, "a change was announced before its event was recorded")
		case workflowhost.EventFinished:
			require.Equal(t, workflowhost.StatusCompleted, ev.Payload.Status, ev.Payload.Message)
			require.Positive(t, announced)
			return
		}
	}
}

// A run's view reloads on every event, so it mustn't read payloads it
// doesn't show, such as file contents and turn replies.
func TestServiceRunViewDoesntReadPayloadsItDoesntShow(t *testing.T) {
	h := newServiceHarness(t, true)
	ctx := h.fixture.Context
	require.NoError(t, h.store.StartRun(ctx, workflowhost.RunRecord{ID: "run-big", Command: "project:demo", InvokingSessionID: h.session}))
	rec := h.store.Recorder("run-big")
	big := json.RawMessage(`"` + strings.Repeat("x", 1<<16) + `"`)
	at := time.Now()
	for _, ev := range []workflow.Event{
		{Seq: 1, Primitive: "readFile", Phase: workflow.PhaseStarted, Args: json.RawMessage(`{"path":"a"}`), At: at},
		{Seq: 1, Primitive: "readFile", Phase: workflow.PhaseCompleted, Result: big, At: at},
		{Seq: 2, Primitive: "ask", Phase: workflow.PhaseStarted, Args: json.RawMessage(`{"question":"q"}`), At: at},
		{Seq: 2, Primitive: "ask", Phase: workflow.PhaseCompleted, Result: json.RawMessage(`"yes"`), At: at},
	} {
		require.NoError(t, rec.Record(ctx, ev))
	}

	view, ok, err := h.svc.RunView(ctx, "run-big")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "yes", view.Phases[0].Items[0].Answer, "the answers it shows still load")
}

func TestServiceReportsARunningRunUntilItEnds(t *testing.T) {
	h := newServiceHarness(t, true)
	h.start(t, h.write(t, `function main() { return ask("go on?") }`), "")
	asked := h.next(t, workflowhost.EventAsk)
	require.True(t, h.svc.Running(h.session))
	require.False(t, h.svc.Running("someone-else"))

	require.NoError(t, h.svc.Respond(asked.RequestID, "yes"))
	h.next(t, workflowhost.EventFinished)
	require.Eventually(t, func() bool { return !h.svc.Running(h.session) }, time.Second, 10*time.Millisecond)
}

// A subagent's surviving jobs stay in its session, so workflow.exclusive
// waits for them (or stops them) like a held session's own.
func TestServiceExclusiveSeesASubagentsSurvivingJobs(t *testing.T) {
	h := newServiceHarness(t, true)
	h.fixture.Provider.Enqueue(branchfixture.Response{Text: "looked"})
	path := h.write(t, `requires({agents: ["reviewer"]})
function main() {
  const s = session.new()
  agent("reviewer", "look")
  ask("go?")
  let refused
  try { workflow.exclusive(() => 1) } catch (e) { refused = e instanceof JobsRunning && e.jobs[0].command }
  return refused
}`)
	h.start(t, path, "")
	ask := h.next(t, workflowhost.EventAsk)
	owned, err := h.fixture.Sessions.ListByWorkflowRun(h.fixture.Context, ask.RunID)
	require.NoError(t, err)
	var sub string
	for _, s := range owned {
		if s.WorkflowKind == session.WorkflowKindAgent {
			sub = s.ID
		}
	}
	require.NotEmpty(t, sub)

	mgr := shell.GetBackgroundShellManager()
	bs, err := mgr.Start(h.fixture.Context, h.dir, nil, "sleep 31", "left by the subagent")
	require.NoError(t, err)
	jobID, err := mgr.Publish(h.fixture.Context, bs.ID(), shell.PublishOptions{SessionID: sub, Origin: shell.OriginExplicit})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Kill(jobID) })
	require.NoError(t, h.svc.Respond(ask.RequestID, "go"))

	done := h.next(t, workflowhost.EventFinished)
	require.Equal(t, workflowhost.StatusCompleted, done.Status, done.Message)
	require.JSONEq(t, `"sleep 31"`, string(done.Result))
}

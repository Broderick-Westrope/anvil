package cmd

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/reload"
	ui "github.com/Broderick-Westrope/anvil/internal/ui/model"
	"github.com/Broderick-Westrope/anvil/internal/version"
	"github.com/stretchr/testify/require"
)

// reloadRecorder records the order of the steps finishReload takes.
type reloadRecorder struct {
	out    bytes.Buffer
	steps  []string
	execed struct {
		exe  string
		args []string
		env  []string
	}
	execErr error
}

func (r *reloadRecorder) Close(clean bool) error {
	r.steps = append(r.steps, "close:"+map[bool]string{true: "clean", false: "dirty"}[clean])
	return nil
}

func (r *reloadRecorder) deps() reloadDeps {
	return reloadDeps{
		out:     &r.out,
		tracker: r,
		cleanup: sync.OnceFunc(func() { r.steps = append(r.steps, "cleanup") }),
		exec: func(exe string, args, env []string) error {
			r.steps = append(r.steps, "exec")
			if !strings.Contains(r.out.String(), "resume with:") {
				return errors.New("resume line not printed before exec")
			}
			r.execed.exe, r.execed.args, r.execed.env = exe, args, env
			return r.execErr
		},
		workDir: "/work",
		dataDir: "/data dir",
		debug:   true,
		now:     func() time.Time { return testHintTime },
	}
}

// testHintTime is a fixed clock for the timestamped resume hints.
var testHintTime = time.Date(2026, 10, 4, 15, 29, 33, 0, time.UTC)

func testReloadRequest() *ui.ReloadRequest {
	return &ui.ReloadRequest{
		Exe:         "/opt/my anvil/anvil",
		SessionID:   "sess-1",
		HandoffPath: "/state/reload/handoff-1.json",
		Yolo:        config.YoloFull,
	}
}

func TestFinishReloadOrder(t *testing.T) {
	t.Parallel()
	r := &reloadRecorder{}
	deps := r.deps()

	require.NoError(t, finishReload(testReloadRequest(), deps))
	deps.cleanup()

	require.Equal(t, []string{"close:clean", "cleanup", "exec"}, r.steps)
	require.Equal(t, "/opt/my anvil/anvil", r.execed.exe)
	require.Equal(t, []string{"--session", "sess-1", "--there", "--data-dir", "/data dir", "--debug", "--yolo=full"}, r.execed.args)
	require.Equal(t,
		"[2026-10-04 15:29:33 UTC] Reloading anvil… if it doesn't come back, resume with:\n"+
			"  ANVIL_RELOAD_HANDOFF=/state/reload/handoff-1.json '/opt/my anvil/anvil' --session sess-1 --there --data-dir '/data dir' --debug --yolo=full\n\n",
		r.out.String())
}

func TestFinishReloadEnv(t *testing.T) {
	t.Parallel()
	r := &reloadRecorder{}
	require.NoError(t, finishReload(testReloadRequest(), r.deps()))

	want := append(reload.StartupEnv(), reload.EnvHandoff+"=/state/reload/handoff-1.json")
	require.Equal(t, want, r.execed.env)
	var handoffVars int
	for _, kv := range r.execed.env {
		if strings.HasPrefix(kv, reload.EnvHandoff+"=") {
			handoffVars++
		}
	}
	require.Equal(t, 1, handoffVars)
}

func TestFinishReloadExecFailure(t *testing.T) {
	t.Parallel()
	r := &reloadRecorder{execErr: errors.New("exec format error")}

	err := finishReload(testReloadRequest(), r.deps())
	require.ErrorIs(t, err, r.execErr)
	require.Contains(t, r.out.String(), "resume with:\n  ANVIL_RELOAD_HANDOFF=")
	require.True(t, strings.HasSuffix(r.out.String(), "Reload failed: exec format error\n"))
}

func TestFinishReloadNilTracker(t *testing.T) {
	t.Parallel()
	r := &reloadRecorder{}
	deps := r.deps()
	deps.tracker = nil

	require.NotPanics(t, func() {
		require.NoError(t, finishReload(testReloadRequest(), deps))
	})
	require.Equal(t, []string{"cleanup", "exec"}, r.steps)
}

type handoffPermsStub struct {
	configured bool
	set        []permission.BouncerMode
}

func (s *handoffPermsStub) PermissionBouncerConfigured() bool { return s.configured }

func (s *handoffPermsStub) PermissionSetBouncerMode(mode permission.BouncerMode) {
	s.set = append(s.set, mode)
}

type handoffReceiverStub struct {
	calls  int
	draft  string
	notice string
	ack    func()
}

func (s *handoffReceiverStub) SetReloadHandoff(draft, notice string, ack func()) {
	s.calls++
	s.draft, s.notice, s.ack = draft, notice, ack
}

func writeTestHandoff(t *testing.T, h reload.Handoff) (string, string) {
	t.Helper()
	dir := reload.Dir(t.TempDir())
	path, err := reload.Write(dir, h)
	require.NoError(t, err)
	return dir, path
}

func TestApplyStartupHandoff(t *testing.T) {
	t.Parallel()
	dir, path := writeTestHandoff(t, reload.Handoff{
		SessionID: "sess-1", Draft: "unsent", BouncerMode: "shadow", FromVersion: "v1",
	})
	perms := &handoffPermsStub{configured: true}
	model := &handoffReceiverStub{}

	applyStartupHandoff(dir, path, "sess-1", perms, model)

	require.Equal(t, []permission.BouncerMode{permission.BouncerShadow}, perms.set)
	require.Equal(t, 1, model.calls)
	require.Equal(t, "unsent", model.draft)
	require.Equal(t, "Reloaded v1 → "+version.Version, model.notice)
	require.FileExists(t, path)
	model.ack()
	require.NoFileExists(t, path)
}

func TestReloadNotice(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Reloaded v1 → v2", reloadNotice("v1", "v2"))
	require.Equal(t, "Reloaded v2 (same version)", reloadNotice("v2", "v2"))
}

func TestApplyStartupHandoffOtherSession(t *testing.T) {
	t.Parallel()
	dir, path := writeTestHandoff(t, reload.Handoff{SessionID: "sess-1", Draft: "unsent", BouncerMode: "shadow"})
	perms := &handoffPermsStub{configured: true}
	model := &handoffReceiverStub{}

	applyStartupHandoff(dir, path, "sess-2", perms, model)

	require.Empty(t, perms.set)
	require.Zero(t, model.calls)
	require.FileExists(t, path)
}

func TestApplyStartupHandoffBouncerNotConfigured(t *testing.T) {
	t.Parallel()
	dir, path := writeTestHandoff(t, reload.Handoff{SessionID: "sess-1", BouncerMode: "enforce"})
	perms := &handoffPermsStub{}
	model := &handoffReceiverStub{}

	applyStartupHandoff(dir, path, "sess-1", perms, model)

	require.Empty(t, perms.set)
	require.Equal(t, 1, model.calls)
}

func TestApplyStartupHandoffInvalid(t *testing.T) {
	t.Parallel()
	dir := reload.Dir(t.TempDir())
	require.NoError(t, os.MkdirAll(dir, 0o700))
	model := &handoffReceiverStub{}

	applyStartupHandoff(dir, "/elsewhere/handoff.json", "sess-1", &handoffPermsStub{}, model)
	applyStartupHandoff(dir, "", "sess-1", &handoffPermsStub{}, model)

	require.Zero(t, model.calls)
}

// TestExecResumeUsesStartupEnv mutates the package-level execAnvil, so it
// can't run in parallel.
func TestExecResumeUsesStartupEnv(t *testing.T) {
	t.Setenv(reload.EnvHandoff, "/tmp/should-not-pass")
	orig := execAnvil
	t.Cleanup(func() { execAnvil = orig })

	var gotExe string
	var gotArgs, gotEnv []string
	execAnvil = func(exe string, args, env []string) error {
		gotExe, gotArgs, gotEnv = exe, args, env
		return nil
	}

	require.NoError(t, execResume("sess-9"))

	exe, err := os.Executable()
	require.NoError(t, err)
	require.Equal(t, exe, gotExe)
	require.Equal(t, []string{"--session", "sess-9", "--there"}, gotArgs)
	require.Equal(t, reload.StartupEnv(), gotEnv)
	for _, kv := range gotEnv {
		require.False(t, strings.HasPrefix(kv, reload.EnvHandoff+"="))
	}
}

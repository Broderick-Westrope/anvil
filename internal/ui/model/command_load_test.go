package model

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

func TestCommandsLoadedWarnsAboutBrokenWorkflows(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	loaded := commandsLoaded([]commands.CustomCommand{
		{ID: "ok", Name: "ok", Workflow: "/ok.js"},
		{ID: "prompt", Name: "prompt", Problems: []string{"prompt commands aren't workflows"}},
		{ID: "project:demo", Name: "demo", Description: "Demo", Workflow: "/w.js", Problems: []string{`skill "x" not found`}},
	})
	infos := f.run(func() tea.Msg { return loaded })

	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Contains(t, infos[0].Msg, "/demo")
	require.Contains(t, infos[0].Msg, `skill "x" not found`)
	require.NotContains(t, infos[0].Msg, "/ok")
	require.NotContains(t, infos[0].Msg, "/prompt")

	var desc string
	for _, item := range f.u.buildSlashACItems() {
		if item.Name == "demo" {
			desc = item.Description
		}
	}
	require.Equal(t, "Demo (broken)", desc)
}

func TestCommandsLoadedWithoutProblemsDoNotWarn(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	infos := f.run(func() tea.Msg {
		return commandsLoaded([]commands.CustomCommand{{ID: "ok", Name: "ok", Workflow: "/ok.js"}})
	})
	require.Empty(t, infos)
	require.Len(t, f.u.customCommands, 1)
}

func TestConfigReloadWarnsAboutBrokenWorkflows(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		report   workspace.ReloadReport
		wantType util.InfoType
	}{
		"reloaded":      {wantType: util.InfoTypeWarn},
		"config failed": {report: workspace.ReloadReport{ConfigErr: errors.New("bad json")}, wantType: util.InfoTypeError},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowUIFixture(t)
			infos := f.run(f.u.handleConfigReloaded(configReloadedMsg{
				Report: tc.report,
				CustomCommands: []commands.CustomCommand{
					{Name: "demo", Workflow: "/w.js", Problems: []string{`agent "ghost" not found`}},
				},
			}))
			require.Len(t, infos, 1)
			require.Equal(t, tc.wantType, infos[0].Type)
			require.Contains(t, infos[0].Msg, `/demo: agent "ghost" not found`)
			require.Contains(t, infos[0].Msg, "reload")
		})
	}
}

// writeProjectWorkflow adds a workflow command to a fresh project
// directory and points the workspace at it. It moves the user config
// directory somewhere empty, so the caller can't run in parallel; the
// commands in ~/.anvil still load, because home.Dir is read at startup.
func (f *workflowUIFixture) writeProjectWorkflow(t *testing.T, name string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "project")
	cmdDir := filepath.Join(dir, "commands", name)
	require.NoError(t, os.MkdirAll(cmdDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cmdDir, "COMMAND.md"), []byte("---\nworkflow: workflow.js\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cmdDir, "workflow.js"), []byte("function main() {}\n"), 0o644))
	f.ws.projectDir = dir
}

func TestInitialCommandLoadAnnotatesAndWarns(t *testing.T) {
	f := newWorkflowUIFixture(t)
	f.writeProjectWorkflow(t, "zz-routing-demo")
	infos := f.run(f.u.loadCustomCommands())

	idx := slices.IndexFunc(f.u.customCommands, func(c commands.CustomCommand) bool { return strings.HasSuffix(c.ID, "zz-routing-demo") })
	require.GreaterOrEqual(t, idx, 0)
	require.Equal(t, []string{`agent "ghost" not found`}, f.u.customCommands[idx].Problems)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Contains(t, infos[0].Msg, `zz-routing-demo: agent "ghost" not found`)
}

func TestConfigReloadAnnotatesCommandsAndWarns(t *testing.T) {
	f := newWorkflowUIFixture(t)
	f.writeProjectWorkflow(t, "zz-routing-demo")
	msg, ok := f.u.runConfigReload().(configReloadedMsg)
	require.True(t, ok)
	infos := f.run(f.u.handleConfigReloaded(msg))

	idx := slices.IndexFunc(f.u.customCommands, func(c commands.CustomCommand) bool { return strings.HasSuffix(c.ID, "zz-routing-demo") })
	require.GreaterOrEqual(t, idx, 0)
	require.Equal(t, []string{`agent "ghost" not found`}, f.u.customCommands[idx].Problems)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Contains(t, infos[0].Msg, `zz-routing-demo: agent "ghost" not found`)
}

// errorRecorder is a slog handler that keeps error records.
type errorRecorder struct{ errors []string }

func (*errorRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *errorRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelError {
		r.errors = append(r.errors, rec.Message)
	}
	return nil
}

func (r *errorRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *errorRecorder) WithGroup(string) slog.Handler      { return r }

// Not parallel: it swaps the default logger.
func TestLoadingCommandsLogsNoErrorWhenItWorks(t *testing.T) {
	rec := &errorRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newWorkflowUIFixture(t)
	f.writeProjectWorkflow(t, "zz-routing-demo")
	loadCommands(f.u.com)
	require.Empty(t, rec.errors)
}

package model

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	uistyles "github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/stretchr/testify/require"
)

// fileOpenTest is a fileOpenFeature over a temp working directory that
// records what it opens instead of opening it.
type fileOpenTest struct {
	*fileOpenFeature
	dir     string
	opened  []string
	edited  []string
	lines   []int
	editErr error
}

func newFileOpenTest(t *testing.T) *fileOpenTest {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ft := &fileOpenTest{dir: dir}
	ft.fileOpenFeature = &fileOpenFeature{
		workingDir: func() string { return dir },
		openURL:    func(u string) error { ft.opened = append(ft.opened, u); return nil },
		editorCmd: func(path string, line int) (*exec.Cmd, error) {
			ft.edited = append(ft.edited, path)
			ft.lines = append(ft.lines, line)
			if ft.editErr != nil {
				return nil, ft.editErr
			}
			return exec.CommandContext(t.Context(), "true"), nil
		},
	}
	return ft
}

func (ft *fileOpenTest) write(t *testing.T, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(ft.dir, name)
	require.NoError(t, os.WriteFile(path, []byte("x"), mode))
	return path
}

func (ft *fileOpenTest) run(t *testing.T, msg tea.Msg) tea.Msg {
	t.Helper()
	cmd, handled := ft.HandleMsg(msg)
	require.True(t, handled)
	return cmd()
}

func requireInfo(t *testing.T, msg tea.Msg, typ util.InfoType, contains string) {
	t.Helper()
	info, ok := msg.(util.InfoMsg)
	require.True(t, ok, "%T", msg)
	require.Equal(t, typ, info.Type, info.Msg)
	require.Contains(t, info.Msg, contains)
}

func TestFileOpenFeatureOpensAFileInTheDefaultApp(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path func(ft *fileOpenTest) string
	}{
		"an absolute path": {path: func(ft *fileOpenTest) string { return filepath.Join(ft.dir, "notes.md") }},
		"a relative path":  {path: func(*fileOpenTest) string { return "notes.md" }},
		"a symlink":        {path: func(ft *fileOpenTest) string { return "link.md" }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)
			target := ft.write(t, "notes.md", 0o644)
			symlink(t, target, filepath.Join(ft.dir, "link.md"))

			msg := ft.run(t, chat.FileOpenMsg{Path: tc.path(ft)})

			requireInfo(t, msg, util.InfoTypeInfo, "Opened notes.md")
			require.Equal(t, []string{fileURL(target)}, ft.opened)
		})
	}
}

func TestFileOpenFeatureWontOpenWhatTheOSWouldRun(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setup func(t *testing.T, ft *fileOpenTest) string
	}{
		"a macOS command": {setup: func(t *testing.T, ft *fileOpenTest) string { return ft.write(t, "run.command", 0o644) }},
		"a Windows program, whatever its case": {
			setup: func(t *testing.T, ft *fileOpenTest) string { return ft.write(t, "setup.EXE", 0o644) },
		},
		"a symlink to a script": {setup: func(t *testing.T, ft *fileOpenTest) string {
			target := ft.write(t, "x.sh", 0o644)
			link := filepath.Join(ft.dir, "notes.md")
			symlink(t, target, link)
			return link
		}},
		"an executable": {setup: func(t *testing.T, ft *fileOpenTest) string {
			if runtime.GOOS == "windows" {
				t.Skip("Windows has no executable bit")
			}
			return ft.write(t, "tool", 0o755)
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)

			msg := ft.run(t, chat.FileOpenMsg{Path: tc.setup(t, ft)})

			requireInfo(t, msg, util.InfoTypeWarn, "since it could run; press e to open it in $EDITOR")
			require.Empty(t, ft.opened)
		})
	}
}

func TestFileOpenFeatureReportsAFileItCantOpen(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path   string
		setup  func(t *testing.T, ft *fileOpenTest)
		reason string
	}{
		"missing": {path: "gone.md", setup: func(*testing.T, *fileOpenTest) {}, reason: "open file:"},
		"a directory": {path: "sub", setup: func(t *testing.T, ft *fileOpenTest) {
			require.NoError(t, os.Mkdir(filepath.Join(ft.dir, "sub"), 0o755))
		}, reason: "is not a regular file"},
		"a dangling symlink": {path: "link", setup: func(t *testing.T, ft *fileOpenTest) {
			symlink(t, filepath.Join(ft.dir, "gone"), filepath.Join(ft.dir, "link"))
		}, reason: "open file:"},
		"no default app": {path: "notes.md", setup: func(t *testing.T, ft *fileOpenTest) {
			ft.write(t, "notes.md", 0o644)
			ft.openURL = func(string) error { return errors.New("no app") }
		}, reason: "open file: no app"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)
			tc.setup(t, ft)

			requireInfo(t, ft.run(t, chat.FileOpenMsg{Path: tc.path}), util.InfoTypeError, tc.reason)
			require.Empty(t, ft.opened)
		})
	}
}

func TestFileOpenFeatureEditsAFileInTheEditor(t *testing.T) {
	t.Parallel()

	ft := newFileOpenTest(t)
	path := ft.write(t, "run.command", 0o755)

	msg := ft.run(t, chat.FileEditMsg{Path: "run.command", Line: 3})

	_, isInfo := msg.(util.InfoMsg)
	require.False(t, isInfo, "it runs the editor, even on what o won't open")
	require.NotNil(t, msg)
	require.Equal(t, []string{path}, ft.edited)
	require.Equal(t, []int{3}, ft.lines)
}

func TestFileOpenFeatureReportsAFileItCantEdit(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path       string
		editErr    error
		reason     string
		editorRuns bool
	}{
		"missing":        {path: "gone.md", reason: "edit file:"},
		"a directory":    {path: ".", reason: "is not a regular file"},
		"no editor runs": {path: "notes.md", editErr: errors.New("snap"), reason: "edit file: snap", editorRuns: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)
			ft.write(t, "notes.md", 0o644)
			ft.editErr = tc.editErr

			requireInfo(t, ft.run(t, chat.FileEditMsg{Path: tc.path}), util.InfoTypeError, tc.reason)
			require.Equal(t, tc.editorRuns, len(ft.edited) == 1)
		})
	}
}

func TestEditorExited(t *testing.T) {
	t.Parallel()

	require.Nil(t, editorExited(nil))
	requireInfo(t, editorExited(errors.New("exit status 1")), util.InfoTypeError, "edit file: exit status 1")
}

func TestEditorCommandGoesToTheLine(t *testing.T) {
	t.Setenv("EDITOR", "vim")

	cmd, err := editorCommand("/a.go", 12)
	require.NoError(t, err)
	require.Equal(t, []string{"vim", "+12", "/a.go"}, cmd.Args)

	cmd, err = editorCommand("/a.go", 0)
	require.NoError(t, err)
	require.Equal(t, []string{"vim", "/a.go"}, cmd.Args)
}

func TestFileOpenFeatureLeavesOtherMessages(t *testing.T) {
	t.Parallel()

	cmd, handled := newFileOpenTest(t).HandleMsg(chat.ImageOpenMsg{})
	require.False(t, handled)
	require.Nil(t, cmd)
}

func TestToolDrillInRoutesFileKeysAndShowsThemInTheHelp(t *testing.T) {
	t.Parallel()

	ws := newWorkflowTestWorkspace()
	u := New(common.DefaultCommon(ws), "", false)
	u.keyMap = DefaultKeyMap()
	ft := newFileOpenTest(t)
	for i, f := range u.features.all {
		if _, ok := f.(*fileOpenFeature); ok {
			u.features.all[i] = ft.fileOpenFeature
		}
	}
	path := ft.write(t, "notes.md", 0o644)
	sty := uistyles.TokyoNight()
	item := chat.NewToolMessageItem(&sty, "msg-1", message.ToolCall{ID: "tc-1", Name: "view", Input: `{"file_path":"` + filepath.ToSlash(path) + `"}`, Finished: true}, &message.ToolResult{ToolCallID: "tc-1", Content: "x"}, false, chat.ToolItemOptions{})
	u.Update(util.ToolDrillInMsg{ToolItem: item, Label: "View"})
	u.state = uiChat
	u.focus = uiFocusMain

	help := helpTexts(u.ShortHelp())
	require.Contains(t, help, "o open file")
	require.Contains(t, help, "e open in $EDITOR")

	_, cmd := u.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	msgs := runCmds(cmd)
	require.Contains(t, msgs, tea.Msg(chat.FileOpenMsg{Path: filepath.ToSlash(path)}))
	_, cmd = u.Update(chat.FileOpenMsg{Path: path})
	runCmds(cmd)
	require.Equal(t, []string{fileURL(path)}, ft.opened)
}

func TestToolImageHelpKeepsOForTheImage(t *testing.T) {
	t.Parallel()

	ws := newWorkflowTestWorkspace()
	u := New(common.DefaultCommon(ws), "", false)
	u.keyMap = DefaultKeyMap()
	u.caps = *kittyCaps()
	sty := uistyles.TokyoNight()
	result := &message.ToolResult{ToolCallID: "tc-1", Data: pngData(t, 4, 4), MIMEType: "image/png"}
	item := chat.NewToolMessageItem(&sty, "msg-1", message.ToolCall{ID: "tc-1", Name: "view", Input: `{"file_path":"a.png"}`, Finished: true}, result, false, chat.ToolItemOptions{})
	u.Update(util.ToolDrillInMsg{ToolItem: item, Label: "View"})
	u.state = uiChat
	u.focus = uiFocusMain
	items := u.activeChat().MessageItems()
	items[len(items)-1].Render(80)

	help := helpTexts(u.ShortHelp())
	require.Contains(t, help, "o open in viewer")
	require.Contains(t, help, "e open in $EDITOR")
	require.NotContains(t, help, "o open file")
}

// symlink links newname to oldname, skipping the test where the OS won't
// let it (Windows without developer mode).
func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("no symlinks: %v", err)
		}
		require.NoError(t, err)
	}
}

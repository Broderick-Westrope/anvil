package model

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	// ran is every command reveal ran, and runErrs what each returns in
	// turn (nil once they run out).
	ran     []revealCommand
	runErrs []error
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
		goos: "darwin",
		runReveal: func(_ context.Context, c revealCommand) error {
			ft.ran = append(ft.ran, c)
			if len(ft.runErrs) == 0 {
				return nil
			}
			err := ft.runErrs[0]
			ft.runErrs = ft.runErrs[1:]
			return err
		},
	}
	return ft
}

func (ft *fileOpenTest) write(t *testing.T, name string, mode os.FileMode) string {
	t.Helper()
	return ft.writeContent(t, name, "x", mode)
}

func (ft *fileOpenTest) writeContent(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(ft.dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
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
		path func(t *testing.T, ft *fileOpenTest, target string) string
	}{
		"an absolute path": {path: func(_ *testing.T, _ *fileOpenTest, target string) string { return target }},
		"a relative path":  {path: func(*testing.T, *fileOpenTest, string) string { return "notes.md" }},
		"a symlink": {path: func(t *testing.T, ft *fileOpenTest, target string) string {
			symlink(t, target, filepath.Join(ft.dir, "link.md"))
			return "link.md"
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)
			target := ft.write(t, "notes.md", 0o644)

			msg := ft.run(t, chat.FileOpenMsg{Path: tc.path(t, ft, target)})

			requireInfo(t, msg, util.InfoTypeInfo, "Opened notes.md")
			require.Equal(t, []string{fileURL(target)}, ft.opened)
		})
	}
}

func TestFileOpenFeatureOpensPassiveTypes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name    string
		content string
	}{
		"Go source":                  {name: "main.go", content: "package main\n"},
		"an empty text file":         {name: "empty.txt", content: ""},
		"an upper-case extension":    {name: "README.MD", content: "# hi\n"},
		"a PNG":                      {name: "a.png", content: "\x89PNG\r\n\x1a\nthe bytes"},
		"a JPEG named .jpeg":         {name: "a.jpeg", content: "\xff\xd8\xffthe bytes"},
		"text longer than the sniff": {name: "long.log", content: strings.Repeat("line\n", 200)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)
			path := ft.writeContent(t, tc.name, tc.content, 0o644)

			requireInfo(t, ft.run(t, chat.FileOpenMsg{Path: tc.name}), util.InfoTypeInfo, "Opened "+tc.name)
			require.Equal(t, []string{fileURL(path)}, ft.opened)
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
		"HTML, which runs in a browser": {setup: func(t *testing.T, ft *fileOpenTest) string { return ft.write(t, "a.html", 0o644) }},
		"SVG, which runs in a browser":  {setup: func(t *testing.T, ft *fileOpenTest) string { return ft.write(t, "a.svg", 0o644) }},
		"a remote desktop file":         {setup: func(t *testing.T, ft *fileOpenTest) string { return ft.write(t, "a.rdp", 0o644) }},
		"no extension":                  {setup: func(t *testing.T, ft *fileOpenTest) string { return ft.write(t, "Makefile", 0o644) }},
		"a trailing dot": {setup: func(t *testing.T, ft *fileOpenTest) string {
			if runtime.GOOS == "windows" {
				t.Skip("Windows drops the dot")
			}
			return ft.write(t, "run.bat.", 0o644)
		}},
		"HTML named as text": {setup: func(t *testing.T, ft *fileOpenTest) string {
			return ft.writeContent(t, "notes.md", "<!DOCTYPE html><script>x</script>", 0o644)
		}},
		"a script named as an image": {setup: func(t *testing.T, ft *fileOpenTest) string {
			return ft.writeContent(t, "a.png", "#!/bin/sh\n", 0o644)
		}},
		"a data stream's name": {setup: func(t *testing.T, ft *fileOpenTest) string {
			if runtime.GOOS == "windows" {
				t.Skip("the colon names a stream, not a file")
			}
			return ft.write(t, "a.bat:x.txt", 0o644)
		}},
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
			return ft.write(t, "tool.txt", 0o744)
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)

			msg := ft.run(t, chat.FileOpenMsg{Path: tc.setup(t, ft)})

			requireInfo(t, msg, util.InfoTypeWarn, "; press e to open it in $EDITOR")
			require.Empty(t, ft.opened)
		})
	}
}

func TestFileOpenFeatureWontTouchANetworkPath(t *testing.T) {
	t.Parallel()

	// Elsewhere a leading backslash starts a relative name, so only the
	// forms that are absolute everywhere reach the check on every OS.
	paths := []string{"//host/share/a.txt", `/\host\share\a.txt`}
	if runtime.GOOS == "windows" {
		paths = append(paths, `\\host\share\a.txt`, `\/host/share/a.txt`, `\??\UNC\host\share\a.txt`)
	}
	for _, path := range paths {
		ft := newFileOpenTest(t)
		requireInfo(t, ft.run(t, chat.FileOpenMsg{Path: path}), util.InfoTypeError, "won't open network path "+path)
		requireInfo(t, ft.run(t, chat.FileEditMsg{Path: path}), util.InfoTypeError, "won't open network path "+path)
		require.Empty(t, ft.opened)
		require.Empty(t, ft.edited)
	}
}

func TestFileOpenFeatureSaysWhyItWontOpen(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name    string
		content string
		why     string
	}{
		"ExtendScript":                      {name: "a.jsx", content: "alert(1)", why: "since only text, source and images open"},
		"a PDF, whose scripts Acrobat runs": {name: "a.pdf", content: "%PDF-1.7\n", why: "since only text, source and images open"},
		"a spreadsheet's formula":           {name: "a.csv", content: "=1+1", why: "since only text, source and images open"},
		"a README that opens with HTML": {
			name: "README.md", content: `<p align="center"><img src="logo.png"></p>`,
			why: "since its contents don't look like .md",
		},
		"an empty image": {name: "a.png", content: "", why: "since its contents don't look like .png"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)
			ft.writeContent(t, tc.name, tc.content, 0o644)

			requireInfo(t, ft.run(t, chat.FileOpenMsg{Path: tc.name}), util.InfoTypeWarn, "Won't open "+tc.name+" in its default app, "+tc.why+"; press e")
			require.Empty(t, ft.opened)
		})
	}
}

// The Windows-only forms can't reach the check through the feature
// elsewhere, where a leading backslash starts a relative name, so the check
// is tested directly too.
func TestIsNetworkPath(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		`\\host\share\a.txt`:       true,
		"//host/share/a.txt":       true,
		`/\host\share\a.txt`:       true,
		`\/host/share/a.txt`:       true,
		`\??\UNC\host\share\a.txt`: true,
		`C:\Users\a.txt`:           false,
		"/home/a.txt":              false,
		`\a.txt`:                   false,
		"/":                        false,
		"":                         false,
		"a//b":                     false,
	}
	for path, want := range tests {
		require.Equal(t, want, isNetworkPath(path), path)
	}
}

func TestFileOpenFeatureReportsAFileItCantOpen(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path   string
		setup  func(t *testing.T, ft *fileOpenTest)
		reason string
	}{
		"missing": {path: "gone.md", setup: func(*testing.T, *fileOpenTest) {}, reason: "gone.md: "},
		"a directory": {path: "sub", setup: func(t *testing.T, ft *fileOpenTest) {
			require.NoError(t, os.Mkdir(filepath.Join(ft.dir, "sub"), 0o755))
		}, reason: "is not a regular file"},
		"a dangling symlink": {path: "link", setup: func(t *testing.T, ft *fileOpenTest) {
			symlink(t, filepath.Join(ft.dir, "gone"), filepath.Join(ft.dir, "link"))
		}, reason: "gone: "},
		"the root": {path: "/", setup: func(*testing.T, *fileOpenTest) {}, reason: "is not a regular file"},
		"unreadable": {path: "secret.txt", setup: func(t *testing.T, ft *fileOpenTest) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows has no write-only mode")
			}
			ft.write(t, "secret.txt", 0o200)
		}, reason: "secret.txt: permission denied"},
		"no default app": {path: "notes.md", setup: func(t *testing.T, ft *fileOpenTest) {
			ft.write(t, "notes.md", 0o644)
			ft.openURL = func(string) error { return errors.New("no app") }
		}, reason: "notes.md: no app"},
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
	symlink(t, path, filepath.Join(ft.dir, "link"))

	msg := ft.run(t, chat.FileEditMsg{Path: "link", Line: 3})

	require.Equal(t, "tea.execMsg", fmt.Sprintf("%T", msg), "it runs the editor, even on what o won't open")
	require.Equal(t, []string{path}, ft.edited, "the file the link resolved to")
	require.Equal(t, []int{3}, ft.lines)
}

func TestFileOpenFeatureReportsAFileItCantEdit(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path    string
		editErr error
		reason  string
		edited  []string
	}{
		"missing":     {path: "gone.md", reason: "gone.md: "},
		"a directory": {path: ".", reason: "is not a regular file"},
		"no editor runs": {
			path: "notes.md", editErr: errors.New("snap"), reason: "notes.md: snap",
			edited: []string{"notes.md"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)
			ft.write(t, "notes.md", 0o644)
			ft.editErr = tc.editErr

			requireInfo(t, ft.run(t, chat.FileEditMsg{Path: tc.path}), util.InfoTypeError, tc.reason)
			var want []string
			for _, name := range tc.edited {
				want = append(want, filepath.Join(ft.dir, name))
			}
			require.Equal(t, want, ft.edited)
		})
	}
}

func TestEditorExited(t *testing.T) {
	t.Parallel()

	require.Nil(t, editorExited("/a.go")(nil))
	requireInfo(t, editorExited("/a.go")(errors.New("exit status 1")), util.InfoTypeError, "edit file /a.go: exit status 1")
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
	require.Contains(t, help, "r "+chat.RevealLabel(runtime.GOOS))

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
	viewer := 0
	for _, h := range help {
		if h == "o open in viewer" {
			viewer++
		}
	}
	require.Equal(t, 1, viewer, "once, though the image item and its file keys both say so")
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

func TestRevealCommands(t *testing.T) {
	t.Parallel()

	dbus := func(uri string) []string {
		return []string{
			"dbus-send", "--session", "--print-reply", "--reply-timeout=5000",
			"--dest=org.freedesktop.FileManager1", "--type=method_call",
			"/org/freedesktop/FileManager1", "org.freedesktop.FileManager1.ShowItems",
			"array:string:" + uri, "string:",
		}
	}
	tests := map[string]struct {
		goos string
		path string
		want []revealCommand
	}{
		"macOS": {goos: "darwin", path: "/a b/n.md", want: []revealCommand{
			{args: []string{"open", "-R", "/a b/n.md"}, shown: "Showed n.md"},
		}},
		"Windows quotes the path after /select,": {goos: "windows", path: `C:\a b,c\n.md`, want: []revealCommand{{
			args:    []string{"explorer", `/select,C:\a b,c\n.md`},
			cmdLine: `explorer /select,"C:\a b,c\n.md"`,
			anyExit: true,
			shown:   "Showed n.md",
		}}},
		"Linux asks over D-Bus, then opens the directory": {goos: "linux", path: "/a,b/n.md", want: []revealCommand{
			{args: dbus("file:///a%2Cb/n.md"), shown: "Showed n.md"},
			{args: []string{"xdg-open", "/a,b"}, detach: true, shown: "Opened /a,b"},
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if tc.goos == "windows" && runtime.GOOS != "windows" {
				// filepath only splits on backslashes on Windows.
				tc.want[0].shown = "Showed " + filepath.Base(tc.path)
			}
			if tc.goos != "windows" && runtime.GOOS == "windows" {
				t.Skip("filepath reads these as Windows paths")
			}
			require.Equal(t, tc.want, revealCommands(tc.goos, tc.path))
		})
	}
}

func TestFileOpenFeatureRevealsTheFileALinkResolvesTo(t *testing.T) {
	t.Parallel()

	ft := newFileOpenTest(t)
	target := ft.write(t, "notes.md", 0o755)
	symlink(t, target, filepath.Join(ft.dir, "link"))

	requireInfo(t, ft.run(t, chat.FileRevealMsg{Path: "link"}), util.InfoTypeInfo, "Showed notes.md")
	require.Equal(t, revealCommands("darwin", target), ft.ran, "executable or not")
}

func TestFileOpenFeatureRevealFallsBackToTheDirectoryOnLinux(t *testing.T) {
	t.Parallel()

	ft := newFileOpenTest(t)
	ft.goos = "linux"
	ft.write(t, "notes.md", 0o644)
	ft.runErrs = []error{errors.New("no FileManager1")}

	requireInfo(t, ft.run(t, chat.FileRevealMsg{Path: "notes.md"}), util.InfoTypeInfo, "Opened "+ft.dir)
	require.Len(t, ft.ran, 2)
	require.Equal(t, []string{"xdg-open", ft.dir}, ft.ran[1].args)
}

func TestFileOpenFeatureReportsEveryRevealThatFailed(t *testing.T) {
	t.Parallel()

	ft := newFileOpenTest(t)
	ft.goos = "linux"
	path := ft.write(t, "notes.md", 0o644)
	notFound := &exec.Error{Name: "xdg-open", Err: exec.ErrNotFound}
	ft.runErrs = []error{errors.New("exit status 1: no FileManager1"), notFound}

	msg := ft.run(t, chat.FileRevealMsg{Path: "notes.md"})

	requireInfo(t, msg, util.InfoTypeError, "reveal file "+path+": dbus-send: exit status 1: no FileManager1\n"+notFound.Error())
}

func TestFileOpenFeatureCountsExplorersExitAsShown(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		goos string
		err  error
		want util.InfoType
	}{
		"explorer exits 1 when it worked": {goos: "windows", err: &exec.ExitError{}, want: util.InfoTypeInfo},
		"explorer missing":                {goos: "windows", err: &exec.Error{Name: "explorer", Err: exec.ErrNotFound}, want: util.InfoTypeError},
		"open -R failing":                 {goos: "darwin", err: &exec.ExitError{}, want: util.InfoTypeError},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ft := newFileOpenTest(t)
			ft.goos = tc.goos
			ft.write(t, "notes.md", 0o644)
			ft.runErrs = []error{tc.err}

			msg, ok := ft.run(t, chat.FileRevealMsg{Path: "notes.md"}).(util.InfoMsg)
			require.True(t, ok)
			require.Equal(t, tc.want, msg.Type, msg.Msg)
		})
	}
}

func TestFileOpenFeatureReportsAFileItCantReveal(t *testing.T) {
	t.Parallel()

	ft := newFileOpenTest(t)
	requireInfo(t, ft.run(t, chat.FileRevealMsg{Path: "gone.md"}), util.InfoTypeError, "reveal file: ")
	requireInfo(t, ft.run(t, chat.FileRevealMsg{Path: "//host/share/a.txt"}), util.InfoTypeError, "reveal file: won't open network path")
	require.Empty(t, ft.ran)
}

// TestRevealHelperProcess is the command runRevealCommand's tests run: the
// test binary, told what to do by the arguments after --.
func TestRevealHelperProcess(t *testing.T) {
	args := flag.Args()
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "fail":
		fmt.Fprintln(os.Stderr, "no FileManager1\nsecond line")
		os.Exit(1)
	case "fail-quietly":
		os.Exit(1)
	case "hang":
		time.Sleep(time.Minute)
	case "linger":
		// It says it started, then waits for the test to say stop, so
		// nothing outlives the test.
		started, stop, exited := args[1], args[2], args[3]
		_ = os.WriteFile(started, nil, 0o600)
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(stop); err == nil {
				break
			}
		}
		_ = os.WriteFile(exited, nil, 0o600)
	}
}

func revealHelper(mode string, args ...string) revealCommand {
	return revealCommand{args: append([]string{os.Args[0], "-test.run=^TestRevealHelperProcess$", "--", mode}, args...)}
}

func TestRunRevealCommand(t *testing.T) {
	t.Parallel()

	require.NoError(t, runRevealCommand(t.Context(), revealHelper("ok")))

	err := runRevealCommand(t.Context(), revealHelper("fail"))
	require.ErrorContains(t, err, "exit status 1: no FileManager1")
	require.NotContains(t, err.Error(), "second line")

	err = runRevealCommand(t.Context(), revealHelper("fail-quietly"))
	require.EqualError(t, err, "exit status 1")

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	require.EqualError(t, runRevealCommand(ctx, revealHelper("hang")), "didn't answer within 10s")

	missing := revealCommand{args: []string{filepath.Join(t.TempDir(), "missing")}}
	require.Error(t, runRevealCommand(t.Context(), missing))
}

func TestRunRevealCommandDoesntWaitOnADetachedCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	started, stop, exited := filepath.Join(dir, "started"), filepath.Join(dir, "stop"), filepath.Join(dir, "exited")
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	t.Cleanup(func() {
		_ = os.WriteFile(stop, nil, 0o600)
		require.Eventually(t, func() bool { return exists(exited) }, 30*time.Second, 20*time.Millisecond)
	})
	linger := revealHelper("linger", started, stop, exited)
	linger.detach = true
	ctx, cancel := context.WithCancel(t.Context())

	require.NoError(t, runRevealCommand(ctx, linger), "it returns once started")
	cancel()

	require.Eventually(t, func() bool { return exists(started) }, 30*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	require.False(t, exists(exited), "cancelling the context didn't kill it")

	missing := revealCommand{args: []string{filepath.Join(t.TempDir(), "missing")}, detach: true}
	require.Error(t, runRevealCommand(t.Context(), missing))
}

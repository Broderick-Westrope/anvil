package model

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/editor"
	"github.com/pkg/browser"
)

// fileOpenFeature opens the file a tool drill-in's call names, in the OS
// default app (chat.FileOpenMsg) or in $EDITOR (chat.FileEditMsg).
type fileOpenFeature struct {
	workingDir func() string
	openURL    func(string) error
	editorCmd  func(path string, line int) (*exec.Cmd, error)
}

func newFileOpenFeature(workingDir func() string) *fileOpenFeature {
	return &fileOpenFeature{
		workingDir: workingDir,
		openURL:    browser.OpenURL,
		editorCmd:  editorCommand,
	}
}

// editorCommand runs $EDITOR on path, at line when it's positive.
func editorCommand(path string, line int) (*exec.Cmd, error) {
	var opts []editor.Option
	if line > 0 {
		opts = append(opts, editor.LineNumber(line))
	}
	return editor.Command("anvil", path, opts...)
}

// Observe implements feature.
func (*fileOpenFeature) Observe(tea.Msg) tea.Cmd { return nil }

// HandleKey implements feature.
func (*fileOpenFeature) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool) { return nil, false }

// HandleAction implements feature.
func (*fileOpenFeature) HandleAction(dialog.Action) (tea.Cmd, bool) { return nil, false }

// HandleMsg implements feature, claiming the open and edit requests.
func (f *fileOpenFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case chat.FileOpenMsg:
		return f.open(f.resolve(msg.Path)), true
	case chat.FileEditMsg:
		return f.edit(f.resolve(msg.Path), msg.Line), true
	}
	return nil, false
}

// resolve makes path absolute against the working directory, as the tools
// do.
func (f *fileOpenFeature) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(f.workingDir(), path)
}

// launchExts are file types the OS runs, installs, or follows to somewhere
// else when it opens them, rather than showing them. The model chose the
// path, so o refuses these, and executable files, on every OS: e still
// opens them as text.
var launchExts = map[string]bool{
	// macOS.
	".app": true, ".command": true, ".tool": true, ".terminal": true,
	".workflow": true, ".action": true, ".pkg": true, ".mpkg": true,
	".dmg": true, ".scpt": true, ".scptd": true, ".applescript": true,
	".webloc": true, ".inetloc": true, ".fileloc": true, ".prefpane": true,
	".mobileconfig": true, ".jar": true,
	// Windows.
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".msi": true,
	".msp": true, ".ps1": true, ".psm1": true, ".vbs": true, ".vbe": true,
	".js": true, ".jse": true, ".wsf": true, ".wsh": true, ".hta": true,
	".scr": true, ".pif": true, ".lnk": true, ".url": true, ".reg": true,
	".cpl": true, ".msc": true, ".application": true, ".appref-ms": true,
	".inf": true, ".scf": true, ".chm": true,
	// Linux, and scripts an interpreter may be registered to run.
	".desktop": true, ".appimage": true, ".run": true, ".deb": true,
	".rpm": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".py": true, ".pyw": true, ".pl": true, ".rb": true,
}

// open opens path in the OS default app, unless it's something the OS
// would run. A symlink is judged, and opened, by what it points at.
func (f *fileOpenFeature) open(path string) tea.Cmd {
	return func() tea.Msg {
		real, info, err := regularFile(path)
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("open file: %w", err))
		}
		if launchExts[strings.ToLower(filepath.Ext(real))] || info.Mode().Perm()&0o111 != 0 {
			return util.NewWarnMsg("Won't open " + filepath.Base(real) + " in its default app, since it could run; press e to open it in $EDITOR")
		}
		if err := f.openURL(fileURL(real)); err != nil {
			return util.NewErrorMsg(fmt.Errorf("open file: %w", err))
		}
		return util.NewInfoMsg("Opened " + filepath.Base(real))
	}
}

// edit opens path in $EDITOR at line, suspending the TUI until it exits.
func (f *fileOpenFeature) edit(path string, line int) tea.Cmd {
	return func() tea.Msg {
		if _, _, err := regularFile(path); err != nil {
			return util.NewErrorMsg(fmt.Errorf("edit file: %w", err))
		}
		cmd, err := f.editorCmd(path, line)
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("edit file: %w", err))
		}
		return tea.ExecProcess(cmd, editorExited)()
	}
}

// editorExited reports $EDITOR failing, and nothing when it exits cleanly.
func editorExited(err error) tea.Msg {
	if err != nil {
		return util.NewErrorMsg(fmt.Errorf("edit file: %w", err))
	}
	return nil
}

// regularFile resolves path's symlinks and checks it ends at a regular
// file, returning where it ends.
func regularFile(path string) (string, os.FileInfo, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", real)
	}
	return real, info, nil
}

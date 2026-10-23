package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/filepathext"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/editor"
	"github.com/pkg/browser"
)

// fileOpenFeature opens the file a tool drill-in's call names, in the OS
// default app (chat.FileOpenMsg), in $EDITOR (chat.FileEditMsg), or in the
// file manager (chat.FileRevealMsg).
type fileOpenFeature struct {
	workingDir func() string
	openURL    func(string) error
	editorCmd  func(path string, line int) (*exec.Cmd, error)
	// goos picks the file manager, and run runs its command.
	goos string
	run  func(ctx context.Context, args []string) error
}

func newFileOpenFeature(workingDir func() string) *fileOpenFeature {
	return &fileOpenFeature{
		workingDir: workingDir,
		openURL:    browser.OpenURL,
		editorCmd:  editorCommand,
		goos:       runtime.GOOS,
		run:        runCommand,
	}
}

// runCommand runs args and waits for it to exit.
func runCommand(ctx context.Context, args []string) error {
	return exec.CommandContext(ctx, args[0], args[1:]...).Run()
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
		path, err := f.resolve(msg.Path)
		if err != nil {
			return util.ReportError(fmt.Errorf("open file: %w", err)), true
		}
		return f.open(path), true
	case chat.FileEditMsg:
		path, err := f.resolve(msg.Path)
		if err != nil {
			return util.ReportError(fmt.Errorf("edit file: %w", err)), true
		}
		return f.edit(path, msg.Line), true
	case chat.FileRevealMsg:
		path, err := f.resolve(msg.Path)
		if err != nil {
			return util.ReportError(fmt.Errorf("reveal file: %w", err)), true
		}
		return f.reveal(path), true
	}
	return nil, false
}

// resolve makes path absolute against the working directory, as the tools
// do. It refuses a network path the model wrote before anything touches
// it: on Windows, reaching a model-chosen \\host\share sends the user's
// credentials to that host. The path as written is checked, not the joined
// one, so a working directory the user put on a share still works. A local
// symlink to a share would still reach it while resolving; making one on
// Windows needs admin rights or Developer Mode.
func (f *fileOpenFeature) resolve(path string) (string, error) {
	if isNetworkPath(path) {
		return "", fmt.Errorf("won't open network path %s", path)
	}
	return filepathext.SmartJoin(f.workingDir(), path), nil
}

// openTypes are the file types o opens in the OS default app, by
// extension, with the type their first bytes must sniff as. The model chose
// the path and may have written the file, and the OS runs, installs,
// mounts or follows many types rather than showing them (scripts, HTML and
// SVG in a browser, disk images, remote-session files), so like imageExts
// this allows known passive types rather than refusing known active ones.
// Some that look passive aren't: .jsx is ExtendScript to Adobe apps, a
// spreadsheet app evaluates formulas in .csv, and Acrobat, a common
// default on Windows, runs a PDF's scripts. The sniff catches a
// passive name on active content, since some openers (xdg-open) go by
// content. Everything else, and anything executable, is left to e, which
// opens it as text.
var openTypes = map[string]string{
	".txt": "text/plain", ".md": "text/plain", ".markdown": "text/plain",
	".log": "text/plain", ".json": "text/plain", ".jsonl": "text/plain",
	".yaml": "text/plain", ".yml": "text/plain", ".toml": "text/plain",
	".go": "text/plain", ".mod": "text/plain", ".sum": "text/plain",
	".rs": "text/plain", ".c": "text/plain", ".h": "text/plain",
	".cc": "text/plain", ".cpp": "text/plain", ".hpp": "text/plain",
	".java": "text/plain", ".kt": "text/plain", ".swift": "text/plain",
	".ts": "text/plain", ".tsx": "text/plain", ".css": "text/plain",
	".scss": "text/plain", ".sql": "text/plain", ".proto": "text/plain",
	".diff": "text/plain", ".patch": "text/plain", ".tf": "text/plain",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp",
}

// open opens path in the OS default app when it's a passive type (see
// openTypes). A symlink is judged, and opened, by what it points at.
func (f *fileOpenFeature) open(path string) tea.Cmd {
	return func() tea.Msg {
		realPath, info, err := localFile(path)
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("open file: %w", err))
		}
		name := filepath.Base(realPath)
		ext := strings.ToLower(filepath.Ext(realPath))
		want, ok := openTypes[ext]
		// A colon names an alternate data stream on Windows, whose own
		// name the extension check would see instead of the file's.
		if !ok || strings.Contains(name, ":") || info.Mode().Perm()&0o111 != 0 {
			return util.NewWarnMsg("Won't open " + name + " in its default app, since only text, source and images open; press e to open it in $EDITOR")
		}
		got, err := sniff(realPath)
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("open file: %w", err))
		}
		if !strings.HasPrefix(got, want) {
			return util.NewWarnMsg("Won't open " + name + " in its default app, since its contents don't look like " + ext + "; press e to open it in $EDITOR")
		}
		if err := f.openURL(fileURL(realPath)); err != nil {
			return util.NewErrorMsg(fmt.Errorf("open file %s: %w", realPath, err))
		}
		return util.NewInfoMsg("Opened " + name)
	}
}

// sniff is the content type of path's first bytes.
func sniff(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close() //nolint:errcheck
	head, err := io.ReadAll(io.LimitReader(file, 512))
	return http.DetectContentType(head), err
}

// edit opens path in $EDITOR at line, suspending the TUI until it exits.
func (f *fileOpenFeature) edit(path string, line int) tea.Cmd {
	return func() tea.Msg {
		realPath, _, err := localFile(path)
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("edit file: %w", err))
		}
		cmd, err := f.editorCmd(realPath, line)
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("edit file %s: %w", realPath, err))
		}
		// Checking the file is IO, so it can't happen in Update; the
		// program runs an exec message whichever command returns it.
		return tea.ExecProcess(cmd, editorExited(realPath))()
	}
}

// editorExited reports $EDITOR failing on path, and nothing when it exits
// cleanly.
func editorExited(path string) tea.ExecCallback {
	return func(err error) tea.Msg {
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("edit file %s: %w", path, err))
		}
		return nil
	}
}

// revealTimeout bounds how long a file manager may take to answer.
const revealTimeout = 10 * time.Second

// revealCommand is a command that shows a file selected in the file
// manager.
type revealCommand struct {
	args []string
	// anyExit counts any exit as shown: explorer /select exits 1 even
	// when it worked.
	anyExit bool
}

// revealCommands are the commands that show path in goos's file manager,
// to try in order. Showing a file opens nothing, so unlike o it needs no
// allowlist. Linux has no one command, so it asks the file manager over
// D-Bus to select the file, then falls back to opening its directory.
func revealCommands(goos, path string) []revealCommand {
	switch goos {
	case "darwin":
		return []revealCommand{{args: []string{"open", "-R", path}}}
	case "windows":
		return []revealCommand{{args: []string{"explorer", "/select," + path}, anyExit: true}}
	default:
		// A comma separates a D-Bus array's items, so it's escaped.
		uri := strings.ReplaceAll(fileURL(path), ",", "%2C")
		return []revealCommand{
			{args: []string{
				"dbus-send", "--session", "--print-reply", "--dest=org.freedesktop.FileManager1",
				"--type=method_call", "/org/freedesktop/FileManager1",
				"org.freedesktop.FileManager1.ShowItems", "array:string:" + uri, "string:",
			}},
			{args: []string{"xdg-open", filepath.Dir(path)}},
		}
	}
}

// reveal shows path selected in the OS file manager, trying each of
// revealCommands until one works.
func (f *fileOpenFeature) reveal(path string) tea.Cmd {
	return func() tea.Msg {
		realPath, _, err := localFile(path)
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("reveal file: %w", err))
		}
		ctx, cancel := context.WithTimeout(context.Background(), revealTimeout)
		defer cancel()
		var failed error
		for _, c := range revealCommands(f.goos, realPath) {
			err := f.run(ctx, c.args)
			var exit *exec.ExitError
			if err == nil || c.anyExit && errors.As(err, &exit) {
				return util.NewInfoMsg("Showed " + filepath.Base(realPath))
			}
			failed = errors.Join(failed, fmt.Errorf("%s: %w", c.args[0], err))
		}
		return util.NewErrorMsg(fmt.Errorf("reveal file %s: %w", realPath, failed))
	}
}

// localFile resolves path's symlinks and checks it ends at a regular
// file, returning where it ends.
func localFile(path string) (string, os.FileInfo, error) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", realPath)
	}
	return realPath, info, nil
}

// isNetworkPath reports whether path is a UNC or device path as Windows
// reads it: two leading separators of either kind, or the \??\ prefix.
// Elsewhere these are odd but local, and refusing them costs nothing.
func isNetworkPath(path string) bool {
	isSep := func(i int) bool { return len(path) > i && (path[i] == '/' || path[i] == '\\') }
	return isSep(0) && isSep(1) || strings.HasPrefix(path, `\??\`)
}

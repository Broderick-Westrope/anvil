package model

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/filepathext"
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
	return filepathext.SmartJoin(f.workingDir(), path)
}

// openTypes are the file types o opens in the OS default app, by
// extension, with the type their first bytes must sniff as. The model chose
// the path and may have written the file, and the OS runs, installs,
// mounts or follows many types rather than showing them (scripts, HTML and
// SVG in a browser, disk images, remote-session files), so like imageExts
// this allows known passive types rather than refusing known active ones.
// Some that look passive aren't: .jsx is ExtendScript to Adobe apps, and a
// spreadsheet app evaluates formulas in .csv. PDF stays, since the default
// viewers (Preview, browsers) run none of its scripts. The sniff catches a
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
	".gif": "image/gif", ".webp": "image/webp", ".pdf": "application/pdf",
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
			return util.NewWarnMsg("Won't open " + name + " in its default app, since only text, source, images and PDFs open; press e to open it in $EDITOR")
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

// localFile resolves path's symlinks and checks it ends at a regular
// file, returning where it ends. It refuses network paths before touching
// them: on Windows, reaching a model-chosen \\host\share sends the user's
// credentials to that host. A local symlink to a share would still reach
// it while resolving; making one on Windows needs admin rights or
// Developer Mode.
func localFile(path string) (string, os.FileInfo, error) {
	if isNetworkPath(path) {
		return "", nil, fmt.Errorf("won't open network path %s", path)
	}
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

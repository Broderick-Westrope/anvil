package chat

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
)

// FileOpenMsg asks for a file a tool call named to be opened in the OS
// default app. Path is as the model wrote it, so it may be relative to the
// working directory.
type FileOpenMsg struct {
	Path string
}

// FileEditMsg asks for a file a tool call named to be opened in $EDITOR,
// at Line when it's positive.
type FileEditMsg struct {
	Path string
	Line int
}

// FileRevealMsg asks for a file a tool call named to be shown in the OS
// file manager (Finder, Explorer).
type FileRevealMsg struct {
	Path string
}

// RevealLabel is what r is called on goos, after its file manager.
func RevealLabel(goos string) string {
	switch goos {
	case "darwin":
		return "reveal in Finder"
	case "windows":
		return "show in Explorer"
	default:
		return "show in file manager"
	}
}

// fileTools are the built-in tools whose file_path is a local file. An MCP
// tool's file_path may name a file elsewhere (a repository, a remote
// host), which opening it here would mistake for a local one.
var fileTools = map[string]bool{
	tools.ViewToolName:        true,
	tools.EditToolName:        true,
	tools.MultiEditToolName:   true,
	tools.WriteToolName:       true,
	tools.DownloadToolName:    true,
	tools.SymbolsToolName:     true,
	tools.DiagnosticsToolName: true,
	tools.RenameToolName:      true,
}

// toolFile is the file a tool drill-in's call names in its file_path
// input. Every item of the drill-in shares one, so o opens the file in the
// OS default app, e opens it in $EDITOR and r shows it in the file manager
// whichever item is selected. It
// reads the call each time it's asked, since the input may still have been
// streaming when the drill-in opened, but parses an input only once: it's
// asked on every render, and a write's input holds the whole file.
type toolFile struct {
	source ToolMessageItem
	// images draws the call's result when it's an image, or is nil.
	images ImageRenderer

	// lastInput is the input path and line were parsed from. Their zero
	// values are what an empty input parses as. A call's tool never
	// changes, only its input as it streams.
	lastInput string
	path      string
	line      int
}

// file is the local path the call names and the line it starts at, or ""
// when it names none.
func (f *toolFile) file() (string, int) {
	tc := f.source.ToolCall()
	if tc.Input != f.lastInput {
		f.path, f.line = parseToolFile(tc.Name, tc.Input)
		f.lastInput = tc.Input
	}
	return f.path, f.line
}

// parseToolFile reads which local file, and line in it, a call to the
// built-in tool name with input names. Skills are read from anvil://
// paths, which aren't files.
func parseToolFile(name, input string) (string, int) {
	if !fileTools[name] {
		return "", 0
	}
	var params struct {
		FilePath string `json:"file_path"`
		// Raw, so a malformed offset doesn't lose the path.
		Offset json.RawMessage `json:"offset"`
	}
	// Malformed input sets nothing, and a file_path that isn't a string
	// stays empty, so both leave no path to open.
	_ = json.Unmarshal([]byte(input), &params)
	if params.FilePath == "" || strings.HasPrefix(params.FilePath, "anvil://") {
		return "", 0
	}
	line := 0
	var offset int
	// View's offset is the 0-based line it starts reading at.
	if name == tools.ViewToolName && json.Unmarshal(params.Offset, &offset) == nil && offset >= 0 {
		line = offset + 1
	}
	return params.FilePath, line
}

// OpenTarget is what o opens from a tool drill-in item.
type OpenTarget int

const (
	// OpensNothing leaves o to the chat.
	OpensNothing OpenTarget = iota
	// OpensFile opens the file the call names in the OS default app.
	OpensFile
	// OpensImage opens the image the call returned in the OS viewer.
	OpensImage
)

// fileKeys is what o, e and r do on every item of a drill-in.
type fileKeys struct {
	open OpenTarget
	// file is whether the call names a local file, which e opens in
	// $EDITOR and r shows in the file manager.
	file bool
}

// keys is what o, e and r do. When the call returned an image, o opens
// that image, as on the output item, so o means one thing throughout the
// drill-in; e and r still act on the file.
func (f *toolFile) keys() fileKeys {
	path, _ := f.file()
	keys := fileKeys{file: path != ""}
	switch {
	case f.image() != nil:
		keys.open = OpensImage
	case keys.file:
		keys.open = OpensFile
	}
	return keys
}

// HandleKeyEvent implements KeyEventHandler.
func (f *toolFile) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	keys := f.keys()
	path, line := f.file()
	var msg tea.Msg
	switch {
	case key.String() == "o" && keys.open == OpensImage:
		msg = ImageOpenMsg{Data: f.image().Data}
	case key.String() == "o" && keys.open == OpensFile:
		msg = FileOpenMsg{Path: path}
	case key.String() == "e" && keys.file:
		msg = FileEditMsg{Path: path, Line: line}
	case key.String() == "r" && keys.file:
		msg = FileRevealMsg{Path: path}
	default:
		return false, nil
	}
	return true, func() tea.Msg { return msg }
}

// FileKeys reports what o opens from a tool drill-in item, and whether
// its call names a local file, which e and r act on.
func FileKeys(item list.Item) (open OpenTarget, file bool) {
	f, ok := item.(interface{ keys() fileKeys })
	if !ok {
		return OpensNothing, false
	}
	keys := f.keys()
	return keys.open, keys.file
}

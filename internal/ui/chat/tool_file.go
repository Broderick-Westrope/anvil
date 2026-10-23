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
// OS default app and e opens it in $EDITOR whichever item is selected. It
// reads the call each time it's asked, since the input may still have been
// streaming when the drill-in opened, but parses an input only once: it's
// asked on every render, and a write's input holds the whole file.
type toolFile struct {
	source ToolMessageItem

	// lastInput is the input path and line were parsed from. Their zero
	// values are what an empty input parses as. A call's tool never
	// changes, only its input as it streams.
	lastInput string
	path      string
	line      int
}

// file is the local path the call names and the line it starts at, or ""
// when it names none. Skills are read from anvil:// paths, which aren't
// files.
func (f *toolFile) file() (string, int) {
	tc := f.source.ToolCall()
	if tc.Input != f.lastInput {
		f.path, f.line = parseToolFile(tc.Name, tc.Input)
		f.lastInput = tc.Input
	}
	return f.path, f.line
}

// parseToolFile is file for a call to the tool name with input.
func parseToolFile(name, input string) (string, int) {
	if !fileTools[name] {
		return "", 0
	}
	// Raw, so the other parameters are skipped rather than copied, and a
	// malformed offset doesn't lose the path.
	var params struct {
		FilePath json.RawMessage `json:"file_path"`
		Offset   json.RawMessage `json:"offset"`
	}
	var path string
	if json.Unmarshal([]byte(input), &params) != nil || json.Unmarshal(params.FilePath, &path) != nil {
		return "", 0
	}
	if path == "" || strings.HasPrefix(path, "anvil://") {
		return "", 0
	}
	line := 0
	var offset int
	// View's offset is the 0-based line it starts reading at.
	if name == tools.ViewToolName && json.Unmarshal(params.Offset, &offset) == nil && offset >= 0 {
		line = offset + 1
	}
	return path, line
}

// HandleKeyEvent implements KeyEventHandler.
func (f *toolFile) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	path, line := f.file()
	if path == "" {
		return false, nil
	}
	var msg tea.Msg
	switch key.String() {
	case "o":
		msg = FileOpenMsg{Path: path}
	case "e":
		msg = FileEditMsg{Path: path, Line: line}
	default:
		return false, nil
	}
	return true, func() tea.Msg { return msg }
}

// OpensFile reports whether item is part of a tool drill-in whose call
// names a file, so o and e open it.
func OpensFile(item list.Item) bool {
	f, ok := item.(interface{ file() (string, int) })
	if !ok {
		return false
	}
	path, _ := f.file()
	return path != ""
}

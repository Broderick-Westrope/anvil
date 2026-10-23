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

// toolFile is the file a tool drill-in's call names in its file_path
// input. Every item of the drill-in embeds one, so o opens the file in the
// OS default app and e opens it in $EDITOR whichever item is selected. It
// reads the call when the key is pressed, since the input may still have
// been streaming when the drill-in opened.
type toolFile struct {
	source ToolMessageItem
}

// file is the path the call names and the line it starts at, or "" when it
// names none on disk. Skills are read from anvil:// paths, which aren't
// files.
func (f toolFile) file() (string, int) {
	tc := f.source.ToolCall()
	// A map, so a malformed parameter beside file_path doesn't lose it.
	var input map[string]any
	if json.Unmarshal([]byte(tc.Input), &input) != nil {
		return "", 0
	}
	path, _ := input["file_path"].(string)
	if path == "" || strings.HasPrefix(path, "anvil://") {
		return "", 0
	}
	line := 0
	// View's offset is the 0-based line it starts reading at.
	if offset, ok := input["offset"].(float64); ok && tc.Name == tools.ViewToolName && offset >= 0 {
		line = int(offset) + 1
	}
	return path, line
}

// HandleKeyEvent implements KeyEventHandler.
func (f toolFile) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
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

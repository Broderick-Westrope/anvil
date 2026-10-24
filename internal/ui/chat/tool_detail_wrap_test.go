package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// Every line of every drill-in item fits the width it was rendered at, and
// none of the text is lost, whatever the tool, so nothing is clipped at the
// screen's edge.
func TestToolDetailItemsFitWidth(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("lorem ipsum dolor sit amet ", 12)
	word := strings.Repeat("abcdefghij", 15)
	multi := long + "\n" + word + "\n" + long
	output := multi + "\n" + strings.Repeat("x", 300)

	tools := []string{
		"bash", "view", "edit", "multiedit", "write", "grep", "glob", "ls",
		"fetch", "agentic_fetch", "download", "sourcegraph", "todos",
		"lsp_diagnostics", "lsp_references", "job_output", "job_list",
		"mcp_server_tool", "task", "unknown_tool",
	}
	inputs := map[string]string{
		"raw":    long + word,
		"params": mustJSON(t, map[string]any{"command": long + word, "file_path": "/tmp/" + word, "content": multi, "count": 3, "nested": map[string]any{"key": long}}),
	}

	for _, name := range tools {
		for inputName, input := range inputs {
			for _, width := range []int{1, 5, 8, 40, 80, 121} {
				for _, expanded := range []bool{false, true} {
					result := &message.ToolResult{ToolCallID: "tc-test", Name: name, Content: output}
					source := helperToolItem(t, name, input, result, ToolStatusSuccess)
					sty := styles.TokyoNight()
					for _, item := range BuildToolDetailItems(&sty, source, nil) {
						if expanded {
							if e, ok := item.(Expandable); ok {
								e.ToggleExpanded()
							}
						}
						for _, focused := range []bool{false, true} {
							if f, ok := item.(interface{ SetFocused(bool) }); ok {
								f.SetFocused(focused)
							}
							for i, line := range strings.Split(item.Render(width), "\n") {
								require.LessOrEqual(t, lipgloss.Width(line), width,
									"tool %s, input %s, width %d, expanded %v: line %d of %s overflows: %q",
									name, inputName, width, expanded, i, item.ID(), line)
							}
						}
					}
				}
			}
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// squash strips styling and all whitespace, so wrapped text compares equal
// to the text it came from.
func squash(s string) string {
	return strings.Join(strings.Fields(ansi.Strip(s)), "")
}

// A long value wraps beside its key, its continuation lines aligned under
// its first, with none of it dropped.
func TestToolDetailParamItemWrapsBesideKey(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	value := strings.Repeat("lorem ipsum dolor ", 10) + strings.Repeat("z", 90)
	item := &toolDetailParamItem{Versioned: list.NewVersioned(), sty: &sty, key: "command", value: value}

	const width = 60
	lines := strings.Split(ansi.Strip(item.Render(width)), "\n")
	require.Greater(t, len(lines), 2, "the value must wrap for this to test anything")
	col := strings.Index(lines[0], "lorem")
	require.Equal(t, len("    command: "), col, "the value starts beside its key")
	for i, line := range lines[1:] {
		require.Equal(t, strings.Repeat(" ", col), line[:col], "line %d isn't aligned under the value", i+1)
		require.NotEqual(t, ' ', line[col], "line %d is indented past the value", i+1)
	}
	require.Equal(t, squash("command:"+value), squash(strings.Join(lines, "\n")))
}

// When the room beside the key is too narrow to read, the value goes on
// the lines below it.
func TestToolDetailParamItemStacksWhenNarrow(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	value := strings.Repeat("abc ", 20)
	item := &toolDetailParamItem{Versioned: list.NewVersioned(), sty: &sty, key: strings.Repeat("k", 20), value: value}

	// The key line is 2+21 columns behind a 2-column bar; 2+21+1+20 is the
	// narrowest width that keeps the value beside it.
	beside := strings.Split(ansi.Strip(item.Render(2+2+21+1+minParamValueWidth)), "\n")
	require.Contains(t, beside[0], "abc")

	stacked := strings.Split(ansi.Strip(item.Render(2+2+21+minParamValueWidth)), "\n")
	require.NotContains(t, stacked[0], "abc")
	require.True(t, strings.HasPrefix(stacked[1], "      abc"), "the value is indented under the key: %q", stacked[1])
	require.Equal(t, squash(item.key+":"+value), squash(strings.Join(stacked, "\n")))
}

// A key too long for the width wraps instead of running off the edge.
func TestToolDetailParamItemWrapsLongKey(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	key := strings.Repeat("k", 50)
	item := &toolDetailParamItem{Versioned: list.NewVersioned(), sty: &sty, key: key, value: "v"}

	out := item.Render(30)
	for _, line := range strings.Split(out, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 30)
	}
	require.Equal(t, squash(key+":v"), squash(out))
}

// Raw input that isn't JSON wraps, indented, with none of it dropped.
func TestToolDetailStaticItemWraps(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("not json ", 30)
	source := helperToolItem(t, "bash", input, nil, ToolStatusSuccess)
	sty := styles.TokyoNight()
	var raw MessageItem
	for _, item := range BuildToolDetailItems(&sty, source, nil) {
		if strings.HasPrefix(item.ID(), "tool-detail-param:raw:") {
			raw = item
		}
	}
	require.NotNil(t, raw)

	lines := strings.Split(ansi.Strip(raw.Render(50)), "\n")
	require.Greater(t, len(lines), 1)
	for _, line := range lines {
		require.True(t, strings.HasPrefix(line, "    not"), "every line is indented: %q", line)
	}
	require.Equal(t, squash(input), squash(strings.Join(lines, "\n")))
}

// Untruncated plain output wraps at the width, keeping its indent and all
// of its text.
func TestToolOutputPlainContentWrapsWhenNotTruncated(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	content := strings.Repeat("word ", 30) + strings.Repeat("y", 70)
	lines := strings.Split(toolOutputPlainContent(&sty, content, 30, true, true), "\n")
	require.Greater(t, len(lines), 2)
	for _, line := range lines {
		require.Equal(t, 30, lipgloss.Width(line))
		require.True(t, strings.HasPrefix(ansi.Strip(line), " "), "every line keeps the indent: %q", line)
		require.NotEqual(t, "  ", ansi.Strip(line)[:2], "no line is indented twice: %q", line)
	}
	require.Equal(t, squash(content), squash(strings.Join(lines, "\n")))
}

// Code lines fill the width exactly, line numbers included.
func TestToolOutputCodeContentFitsWidth(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	content := strings.Repeat("abcdefghij", 10) + "\nshort"
	for _, noTruncate := range []bool{false, true} {
		out := toolOutputCodeContent(&sty, "", content, 0, 40, true, noTruncate)
		for _, line := range strings.Split(out, "\n") {
			require.Equal(t, 40, lipgloss.Width(line), "noTruncate %v: %q", noTruncate, ansi.Strip(line))
		}
	}
}

// A collapsed multi-line value says how many lines it hides.
func TestToolDetailParamItemCollapsedHintCountsLines(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	item := &toolDetailParamItem{Versioned: list.NewVersioned(), sty: &sty, key: "content", value: "one\ntwo\nthree"}

	require.Contains(t, ansi.Strip(item.Render(80)), "content: "+fmt.Sprintf(assistantMessageTruncateFormat, 3))
}

// Truncated plain output cuts a long line short on one line, with an
// ellipsis, and leaves a line that fits alone.
func TestToolOutputPlainContentTruncatesLongLines(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	long := strings.Repeat("word ", 30)
	lines := strings.Split(toolOutputPlainContent(&sty, long+"\nshort", 30, true, false), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, " "+strings.TrimSpace(ansi.Truncate(long, 28, ""))+"…", strings.TrimRight(ansi.Strip(lines[0]), " "))
	require.Equal(t, " short", strings.TrimRight(ansi.Strip(lines[1]), " "))
	for _, line := range lines {
		require.Equal(t, 30, lipgloss.Width(line))
	}
}

// A key that wraps keeps its colour on every line.
func TestToolDetailParamItemWrappedKeyKeepsColour(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	key := "some very long key with spaces in it that wraps"
	item := &toolDetailParamItem{Versioned: list.NewVersioned(), sty: &sty, key: key, value: "v"}

	colour := sgrPrefix(t, sty.Tool.NameNested.Render("x"))
	require.NotEmpty(t, colour)
	lines := strings.Split(item.Render(30), "\n")
	var keyLines int
	for _, line := range lines {
		if text := strings.TrimSpace(ansi.Strip(line)); text != "" && text != "v" {
			keyLines++
			require.Contains(t, line, colour+strings.Fields(text)[0], "a key line lost its colour: %q", line)
		}
	}
	require.Greater(t, keyLines, 1, "the key must wrap for this to test anything")
}

// Coloured output that wraps keeps its colour on every line.
func TestToolOutputPlainContentWrappedKeepsColour(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	const red = "\x1b[31m"
	lines := strings.Split(toolOutputPlainContent(&sty, red+strings.Repeat("red ", 20)+"\x1b[0m", 30, true, true), "\n")
	require.Greater(t, len(lines), 1, "the output must wrap for this to test anything")
	for i, line := range lines {
		require.Contains(t, line, red+"red", "line %d lost its colour: %q", i, line)
	}
}

// A key that wraps puts its value on the lines below it, even when its
// widest line leaves room beside it.
func TestToolDetailParamItemStacksUnderWrappedKey(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	word := "abcdefghijklmnopqrstuvwxyz0123456789ab"
	item := &toolDetailParamItem{Versioned: list.NewVersioned(), sty: &sty, key: word + " " + word, value: "value"}

	// Two words don't fit in 80 columns, so the key takes a line each, 40
	// columns wide, leaving 39 beside them.
	lines := strings.Split(ansi.Strip(item.Render(80)), "\n")
	require.Len(t, lines, 3)
	require.Equal(t, "      value", strings.TrimRight(lines[2], " "))
}

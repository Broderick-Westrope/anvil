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

// Every line of every drill-in item fits the width it was rendered at,
// whatever the tool, its state, its input and its result, so nothing is
// clipped at the screen's edge.
func TestToolDetailItemsFitWidth(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("lorem ipsum dolor sit amet ", 12)
	word := strings.Repeat("abcdefghij", 15)
	multi := long + "\n" + word + "\n" + long
	output := multi + "\n" + strings.Repeat("x", 300) + "\n10%\r50%\r100%\tdone\vpage\fnext"
	// Edits read the old and new content, writes a unified diff. Tools
	// that don't read metadata ignore it.
	metadata := map[string]string{
		"plain": "",
		"diff": mustJSON(t, map[string]any{
			"old_content": multi + "\r\nold\rline\tend",
			"new_content": multi + "\nadded " + long + "\r\nnew\rline\tend",
			"diff":        "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-" + long + "\r\n+" + word + "\tx\ry",
		}),
		"hook": mustJSON(t, map[string]any{"hook": map[string]any{
			"hook_count": 1, "decision": "deny",
			"hooks": []any{map[string]any{
				"name": "fmt\thook\r\n" + word, "matcher": "ba\tsh\r", "decision": "deny",
				"reason": "because\r\n" + long + "\t" + word,
			}},
		}}),
	}

	// Agents, agentic fetch and todos have no tool drill-in.
	tools := []string{
		"bash", "view", "edit", "multiedit", "write", "grep", "glob", "ls",
		"fetch", "download", "sourcegraph", "lsp_diagnostics",
		"lsp_references", "job_output", "job_list", "mcp_server_tool",
		"unknown_tool", "mcp_server_odd\r\nname\rwith\ttab",
	}
	inputs := map[string]string{
		"raw":              long + word,
		"raw with returns": "first\rsecond\r\nthird\t\v\f" + long,
		"returns":          mustJSON(t, map[string]any{"command": "first\rsecond\t" + long, "content": "a\r\nb\rc", "odd\r\nkey\rwith\ttab": "v", "shell_id": "AB\r\nSECOND\rTHIRD\tX" + word}),
		"params":           mustJSON(t, map[string]any{"command": long + word, "file_path": "/tmp/" + word, "content": multi, "count": 3, "nested": map[string]any{"key": long}}),
	}
	statuses := map[string]ToolStatus{
		"success":             ToolStatusSuccess,
		"error":               ToolStatusError,
		"awaiting permission": ToolStatusAwaitingPermission,
	}

	for _, name := range tools {
		for inputName, input := range inputs {
			for statusName, status := range statuses {
				for resultName, meta := range metadata {
					for _, expanded := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/%s/%s/expanded=%v", name, inputName, statusName, resultName, expanded), func(t *testing.T) {
							t.Parallel()
							var result *message.ToolResult
							if status != ToolStatusAwaitingPermission {
								result = &message.ToolResult{ToolCallID: "tc-test", Name: name, Content: output, Metadata: meta, IsError: status == ToolStatusError}
							}
							// A diff lays out side by side once it is wide
							// enough, so its text can't be compared across
							// widths.
							requireDetailItemsFit(t, helperToolItem(t, name, input, result, status), expanded, resultName != "diff")
						})
					}
				}
			}
		}
	}
}

// requireDetailItemsFit renders each of source's drill-in items, focused
// and not, at a range of widths, and fails on any line wider than one. With
// checkText, from 40 columns up, no item but the header and dividers (which
// drop hints and rule characters by design) may drop text to fit: a cut
// line would pass the width check, so its text is compared with a render
// wide enough to need no wrapping.
func requireDetailItemsFit(t *testing.T, source ToolMessageItem, expanded, checkText bool) {
	t.Helper()
	sty := styles.TokyoNight()
	for _, item := range BuildToolDetailItems(&sty, source, nil) {
		if e, ok := item.(Expandable); ok && expanded {
			e.ToggleExpanded()
		}
		for _, focused := range []bool{false, true} {
			if f, ok := item.(interface{ SetFocused(bool) }); ok {
				f.SetFocused(focused)
			}
			whole := squashBody(item.Render(2000))
			for _, width := range []int{1, 5, 8, 40, 80, 121} {
				rendered := item.Render(width)
				require.NotContains(t, rendered, "\r", "width %d, focused %v: %s sends a carriage return", width, focused, item.ID())
				require.NotContains(t, rendered, "\t", "width %d, focused %v: %s sends a tab, which is measured as no columns", width, focused, item.ID())
				require.False(t, strings.ContainsAny(rendered, "\v\f"), "width %d, focused %v: %s sends a vertical tab or form feed", width, focused, item.ID())
				for i, line := range strings.Split(rendered, "\n") {
					require.LessOrEqual(t, lipgloss.Width(line), width,
						"width %d, focused %v: line %d of %s overflows: %q", width, focused, i, item.ID(), line)
				}
				if _, ruled := item.(*toolDetailSectionItem); ruled || !checkText {
					continue
				}
				if _, header := item.(*toolDetailHeaderItem); !header && width >= 40 {
					require.Equal(t, whole, squashBody(rendered), "width %d, focused %v: %s lost text", width, focused, item.ID())
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

// squashBody squashes rendered without the focus bar, which is drawn once
// per line, so a wrapped render has more of it.
func squashBody(rendered string) string {
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		lines[i] = ansi.Cut(line, MessageLeftPaddingTotal, lipgloss.Width(line))
	}
	return squash(strings.Join(lines, "\n"))
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
		require.NotEqual(t, byte(' '), line[col], "line %d is indented past the value", i+1)
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
	lines := strings.Split(ansi.Strip(out), "\n")
	for _, line := range lines {
		require.LessOrEqual(t, lipgloss.Width(line), 30)
	}
	// Past the focus bar, every line of the key starts in the same column,
	// and the value goes below it.
	for i, line := range lines[:len(lines)-1] {
		require.True(t, strings.HasPrefix(line[MessageLeftPaddingTotal:], "  k"), "line %d: %q", i, line)
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

// Untruncated plain output wraps each of its lines at the width, keeping
// its indent and all of its text.
func TestToolOutputPlainContentWrapsWhenNotTruncated(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	content := strings.Repeat("y", 70) + " " + strings.Repeat("word ", 30) + "\nsecond line"
	lines := strings.Split(toolOutputPlainContent(&sty, content, 30, true, true), "\n")
	require.Equal(t, " "+strings.Repeat("y", 29), ansi.Strip(lines[0]), "a wrapped line fills the width past its indent")
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

// A parameter holding a line break or tab keeps a collapsed header on one
// line within its width, so none of it spills into the drill-in's Output,
// and no carriage return reaches the terminal.
func TestCollapsedHeaderFlattensWhitespace(t *testing.T) {
	t.Parallel()

	for name, pattern := range map[string]string{
		"newline":         "first\nPATSECOND",
		"crlf":            "first\r\nPATSECOND",
		"carriage return": "first\rPATSECOND",
		"vertical tab":    "first\vPATSECOND",
		"form feed":       "first\fPATSECOND",
		"tab":             "first\t" + strings.Repeat("x", 60) + "PATSECOND",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := &message.ToolResult{ToolCallID: "tc-test", Name: "grep", Content: "out"}
			source := helperToolItem(t, "grep", mustJSON(t, map[string]any{"pattern": pattern, "path": "/tmp"}), result, ToolStatusSuccess)
			requireOneLineHeader(t, source, pattern)

			sty := styles.TokyoNight()
			items := BuildToolDetailItems(&sty, source, nil)
			require.NotContains(t, ansi.Strip(items[len(items)-1].Render(80)), "PATSECOND")
		})
	}
}

// A background bash job's header holds its command to one line too, so
// the drill-in's Output starts with its own body.
func TestJobHeaderFlattensWhitespace(t *testing.T) {
	t.Parallel()

	command := "echo first\r\nPATSECOND"
	result := &message.ToolResult{
		ToolCallID: "tc-test", Name: "bash", Content: "out",
		Metadata: mustJSON(t, map[string]any{"background": true, "shell_id": "abc"}),
	}
	input := mustJSON(t, map[string]any{"command": command, "run_in_background": true})
	source := helperToolItem(t, "bash", input, result, ToolStatusSuccess)
	requireOneLineHeader(t, source, command)

	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source, nil)
	output := strings.TrimSpace(ansi.Strip(items[len(items)-1].Render(80)))
	require.True(t, strings.HasPrefix(output, "Command:"), "the header spilled into Output: %q", output)
}

// requireOneLineHeader checks source's collapsed header is one line within
// 80 columns, with no carriage return, showing raw with each line break as
// one space and each tab as four.
func requireOneLineHeader(t *testing.T, source ToolMessageItem, raw string) {
	t.Helper()

	rendered := source.Render(80)
	require.NotContains(t, rendered, "\r")
	require.LessOrEqual(t, lipgloss.Width(rendered), 80)
	header := strings.Split(ansi.Strip(rendered), "\n")
	require.Len(t, header, 1)
	flat := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\v", " ", "\f", " ", "\t", "    ").Replace(raw)
	require.Contains(t, header[0], ansi.Truncate(flat, 20, ""))
}

// Rendering a drill-in leaves its source as it was, so the item in the
// chat still wraps its header over every line it needs when expanded.
func TestToolDetailOutputLeavesSourceHeaderExpandable(t *testing.T) {
	t.Parallel()

	command := "echo " + strings.Repeat("lorem ipsum ", 12)
	result := &message.ToolResult{ToolCallID: "tc-test", Name: "bash", Content: "out"}
	source := helperToolItem(t, "bash", mustJSON(t, map[string]any{"command": command}), result, ToolStatusSuccess)

	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source, nil)
	items[len(items)-1].Render(60)

	source.(Expandable).ToggleExpanded()
	require.Contains(t, squash(source.Render(60)), squash(command))
}

// Rendering a drill-in leaves an item the user expanded in the chat
// showing its full command.
func TestToolDetailOutputKeepsSourceExpanded(t *testing.T) {
	t.Parallel()

	command := "echo " + strings.Repeat("lorem ipsum ", 12)
	result := &message.ToolResult{ToolCallID: "tc-test", Name: "bash", Content: "out"}
	source := helperToolItem(t, "bash", mustJSON(t, map[string]any{"command": command}), result, ToolStatusSuccess)
	source.(Expandable).ToggleExpanded()
	require.Contains(t, squash(source.Render(60)), squash(command), "expanded, the chat item shows the full command")

	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source, nil)
	items[len(items)-1].Render(60)

	require.Contains(t, squash(source.Render(60)), squash(command))
}

// A collapsed header measures its parameters with tabs widened, so a pair
// that won't fit beside the main parameter once drawn is left out.
func TestToolParamListMeasuresFlattenedParams(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	// Drawn, "path=a" and six tabs take 30 columns, leaving 40-30-3 = 7 for
	// the main parameter, under the 30 it needs.
	out := toolParamList(&sty, []string{"main", "path", "a" + strings.Repeat("\t", 6)}, 40, nil)
	require.Equal(t, "main", ansi.Strip(out))
}

// An expanded header keeps a parameter's line breaks, a carriage return
// among them, without sending one to the terminal, and fits a tab's drawn
// width.
func TestExpandedHeaderBreaksCarriageReturns(t *testing.T) {
	t.Parallel()

	for name, pattern := range map[string]string{
		"crlf":            "first\r\nPATSECOND",
		"carriage return": "first\rPATSECOND",
		"tab":             "first\nPATSECOND" + strings.Repeat("\t", 20) + "x",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := &message.ToolResult{ToolCallID: "tc-test", Name: "grep", Content: "out"}
			source := helperToolItem(t, "grep", mustJSON(t, map[string]any{"pattern": pattern, "path": "/tmp"}), result, ToolStatusSuccess)
			source.(Expandable).ToggleExpanded()

			rendered := source.Render(80)
			require.NotContains(t, rendered, "\r")
			for _, line := range strings.Split(rendered, "\n") {
				require.LessOrEqual(t, lipgloss.Width(line), 80)
			}
			lines := strings.Split(ansi.Strip(rendered), "\n")
			require.True(t, strings.HasSuffix(strings.TrimRight(lines[0], " "), "first"), "the header breaks after the first line: %q", lines[0])
			require.Contains(t, lines[1], "PATSECOND")
		})
	}
}

// Rendering a drill-in leaves a compact, expanded item in the chat looking
// as it did.
func TestToolDetailOutputRestoresSourceRenderState(t *testing.T) {
	t.Parallel()

	pattern := strings.Repeat("lorem ipsum ", 12)
	result := &message.ToolResult{ToolCallID: "tc-test", Name: "grep", Content: "out"}
	source := helperToolItem(t, "grep", mustJSON(t, map[string]any{"pattern": pattern}), result, ToolStatusSuccess)
	source.(Expandable).ToggleExpanded()
	source.(Compactable).SetCompact(true)
	before := source.Render(60)

	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source, nil)
	items[len(items)-1].Render(60)

	require.Equal(t, before, source.Render(60))
}

// Headers, breadcrumbs and job notices built from text the model or a
// server wrote hold it to one line, with tabs widened and no carriage
// return.
func TestOtherHeaderTextFlattensWhitespace(t *testing.T) {
	t.Parallel()

	const text = "first\r\nsecond\rthird\tfourth"
	// A notice is split into lines first, so its text can't hold a line
	// feed.
	const line = "first second\rthird\tfourth"
	sty := styles.TokyoNight()

	agent := helperToolItem(t, "task", mustJSON(t, map[string]any{"description": text, "prompt": "p"}), nil, ToolStatusRunning)
	fetch := helperToolItem(t, "agentic_fetch", mustJSON(t, map[string]any{"prompt": text}), nil, ToolStatusRunning)
	mcp := helperToolItem(t, "mcp_server_"+text, `{}`, nil, ToolStatusSuccess)
	pending := helperToolItem(t, "mcp_server_"+text, ``, nil, ToolStatusRunning)

	for name, rendered := range map[string]string{
		"agent header":      strings.Split(agent.Render(120), "\n")[0],
		"agent breadcrumb":  agent.(AgentDrillInHandler).AgentDrillInLabel(),
		"fetch breadcrumb":  fetch.(AgentDrillInHandler).AgentDrillInLabel(),
		"tool name":         mcp.Render(120),
		"tool breadcrumb":   mcp.(ToolDrillInHandler).ToolDrillInLabel(),
		"pending tool name": pending.Render(120),
		"image type":        toolOutputImageContent(&sty, "AAAA", text),
		"skill":             toolOutputSkillContent(&sty, text, text),
		"job unknown event": renderJobNotice(&sty, "- "+line, 120, false),
		"job watch":         renderJobNotice(&sty, "- Job 002 printed a line matching your watch ("+line+"): x", 120, false),
		"job description":   renderJobNotice(&sty, "- Job 002 ended, killed (15s): "+line+".", 120, false),
		"job tail":          renderJobNotice(&sty, "- Job 002 ended, killed (15s): x.\n  "+line, 120, true),
		"job other line":    renderJobNotice(&sty, line, 120, false),
	} {
		require.NotContains(t, rendered, "\r", name)
		require.NotContains(t, rendered, "\t", name)
		words := strings.ToLower(strings.Join(strings.Fields(ansi.Strip(rendered)), " "))
		require.Contains(t, words, "first second third fourth", name)
	}
}

// A drill-in shows an error in full, wrapped beside its tag, where the chat
// cuts it to one line.
func TestToolDetailOutputWrapsErrors(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("something went wrong ", 10) + "END"
	result := &message.ToolResult{ToolCallID: "tc-test", Name: "bash", Content: text, IsError: true}
	source := helperToolItem(t, "bash", `{"command":"false"}`, result, ToolStatusError)

	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source, nil)
	var lines []string
	for _, line := range strings.Split(ansi.Strip(items[len(items)-1].Render(60)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	require.Greater(t, len(lines), 1, "the error must wrap for this to test anything")
	require.Contains(t, lines[0], "ERROR")
	col := strings.Index(lines[0], "something")
	require.Greater(t, col, strings.Index(lines[0], "ERROR"))
	for i, line := range lines[1:] {
		require.Equal(t, strings.Repeat(" ", col), line[:col], "line %d isn't aligned under the message", i+1)
		require.NotEqual(t, byte(' '), line[col], "line %d is indented past the message", i+1)
	}
	require.Contains(t, squash(strings.Join(lines, "\n")), squash(text))
}

// Cut to one line, an error leaves the same margin it always has.
func TestToolErrorContentCutsToOneLine(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	for tag, text := range map[string]string{
		"ERROR": strings.Repeat("boom ", 30),
		"WARN":  "User denied permission " + strings.Repeat("x", 100),
	} {
		out := toolErrorContent(&sty, &message.ToolResult{Content: text}, 60)
		require.True(t, strings.HasPrefix(strings.TrimSpace(ansi.Strip(out)), tag), "%q", ansi.Strip(out))
		require.NotContains(t, out, "\n")
		require.Equal(t, 58, lipgloss.Width(out), "%q", ansi.Strip(out))
		require.True(t, strings.HasSuffix(strings.TrimRight(ansi.Strip(out), " "), "…"))
	}
}

// A job header with a long ID stays within the chat's width.
func TestJobHeaderFitsWidth(t *testing.T) {
	t.Parallel()

	result := &message.ToolResult{ToolCallID: "tc-test", Name: "job_kill", Content: "killed"}
	source := helperToolItem(t, "job_kill", mustJSON(t, map[string]any{"shell_id": strings.Repeat("A", 200)}), result, ToolStatusSuccess)
	for _, line := range strings.Split(source.Render(80), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 80)
	}
}

// A drill-in's Output keeps the hooks that ran, wrapping a denial's whole
// reason, and drops the tool's header rather than them.
func TestToolDetailOutputKeepsHookLines(t *testing.T) {
	t.Parallel()

	reason := strings.Repeat("not allowed here ", 8) + "END"
	metadata := mustJSON(t, map[string]any{"hook": map[string]any{
		"hook_count": 1, "decision": "deny",
		"hooks": []any{map[string]any{"name": "guard", "decision": "deny", "reason": reason}},
	}})
	result := &message.ToolResult{ToolCallID: "tc-test", Name: "grep", Content: "body", Metadata: metadata}
	source := helperToolItem(t, "grep", `{"pattern":"PATTERNHEAD"}`, result, ToolStatusSuccess)

	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source, nil)
	output := ansi.Strip(items[len(items)-1].Render(60))
	require.NotContains(t, output, "PATTERNHEAD", "the header spilled into Output")
	require.True(t, strings.HasPrefix(strings.TrimSpace(output), "Hook guard"), "%q", output)
	require.Contains(t, squash(output), squash(reason))
	require.Contains(t, output, "body")

	lines := strings.Split(output, "\n")
	blank := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blank++
		}
	}
	require.Equal(t, 1, blank, "one blank line between the hooks and the output: %q", output)
	col := lipgloss.Width(lines[0][:strings.Index(lines[0], "Denied")])
	require.Greater(t, col, 0)
	require.Equal(t, strings.Repeat(" ", col), lines[1][:col], "the reason wraps under where the detail starts: %q", lines[1])
	require.NotEqual(t, byte(' '), lines[1][col], "the reason wraps no further in than where the detail starts: %q", lines[1])

	header := strings.Split(ansi.Strip(source.Render(60)), "\n")
	require.True(t, strings.HasPrefix(strings.TrimSpace(header[0]), "Hook "), "the chat still shows the hooks above the header: %q", header[0])
	require.True(t, strings.HasSuffix(strings.TrimRight(header[0], " "), "…"), "the chat cuts the reason to one line: %q", header[0])
}

// A hook's detail wraps beside its name while that leaves room to read it,
// and goes on the lines below, indented, once it doesn't.
func TestHookLineWrapsOnlyWithRoom(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	reason := strings.Repeat("nope ", 20)
	metadata := hookMetadata(t, map[string]any{"name": "g", "decision": "deny", "reason": reason})

	// "Hook g → " puts the detail at column 9.
	const col = 9
	beside := strings.Split(ansi.Strip(toolOutputHookIndicator(&sty, metadata, col+minParamValueWidth, true)), "\n")
	require.Greater(t, len(beside), 1)
	require.True(t, strings.HasPrefix(beside[0], "Hook g → Denied"), "%q", beside[0])
	require.Equal(t, squash("Hook g → Denied "+reason), squash(strings.Join(beside, "\n")))

	below := strings.Split(ansi.Strip(toolOutputHookIndicator(&sty, metadata, col+minParamValueWidth-1, true)), "\n")
	for _, line := range below {
		require.LessOrEqual(t, lipgloss.Width(line), col+minParamValueWidth-1)
	}
	require.Equal(t, "Hook g →", below[0])
	require.True(t, strings.HasPrefix(below[1], "    Denied"), "%q", below[1])
	require.Equal(t, squash("Hook g → Denied "+reason), squash(strings.Join(below, "\n")))
}

// An expanded header draws a tab as four spaces.
func TestExpandedHeaderWidensTabs(t *testing.T) {
	t.Parallel()

	result := &message.ToolResult{ToolCallID: "tc-test", Name: "grep", Content: "out"}
	source := helperToolItem(t, "grep", mustJSON(t, map[string]any{"pattern": "first\nP\t\tQ"}), result, ToolStatusSuccess)
	source.(Expandable).ToggleExpanded()

	require.Contains(t, ansi.Strip(source.Render(80)), "P        Q")
}

// hookMetadata is a tool result's metadata for one hook that ran.
func hookMetadata(t *testing.T, hook map[string]any) string {
	t.Helper()
	return mustJSON(t, map[string]any{"hook": map[string]any{
		"hook_count": 1, "decision": hook["decision"], "hooks": []any{hook},
	}})
}

// The chat cuts a hook's name before its denial reason, since the reason
// says why; the drill-in, which wraps the reason, keeps the name whole.
func TestHookLineRoomForReason(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	const name = "/usr/local/bin/hooks/guard.sh"
	const reason = "rm -rf is not allowed in this repository, use trash"
	metadata := hookMetadata(t, map[string]any{"name": name, "decision": "deny", "reason": reason})

	chat := ansi.Strip(toolOutputHookIndicator(&sty, metadata, 80, false))
	require.Contains(t, chat, "Denied "+reason)
	require.NotContains(t, chat, name)
	require.Equal(t, 80, lipgloss.Width(chat), "the name takes all the room left: %q", chat)

	drill := ansi.Strip(toolOutputHookIndicator(&sty, metadata, 80, true))
	require.Contains(t, drill, name)
	require.Contains(t, squash(drill), squash(reason))
}

// A long matcher gives way to the decision and reason in the chat, and in
// the drill-in neither is lost.
func TestHookLineHoldsMatcherAndReason(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	matcher := strings.Repeat("Bash|Edit|", 8)
	metadata := hookMetadata(t, map[string]any{
		"name": "g", "matcher": matcher, "decision": "deny", "reason": "not\r\nhere\vnow",
	})

	chat := ansi.Strip(toolOutputHookIndicator(&sty, metadata, 80, false))
	require.NotContains(t, chat, "\n")
	require.Equal(t, 80, lipgloss.Width(chat))
	require.True(t, strings.HasSuffix(chat, "… → Denied not here now"), "%q", chat)

	drill := ansi.Strip(toolOutputHookIndicator(&sty, metadata, 80, true))
	require.Contains(t, squash(drill), squash("Hook g "+matcher+" → Denied not here now"))
	for _, line := range strings.Split(drill, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 80)
	}
}

// Hooks line their arrows up, padding a hook without a matcher to the
// width of one with.
func TestHookLinesAlignMatchers(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	metadata := mustJSON(t, map[string]any{"hook": map[string]any{
		"hook_count": 2, "decision": "allow",
		"hooks": []any{
			map[string]any{"name": "a", "matcher": "Bash", "decision": "allow"},
			map[string]any{"name": "b", "decision": "allow"},
		},
	}})
	lines := strings.Split(ansi.Strip(toolOutputHookIndicator(&sty, metadata, 80, false)), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, "Hook a Bash → OK", lines[0])
	require.Equal(t, "Hook b      → OK", lines[1])
}

// With no width to fit, a hook line is left whole.
func TestHookLineWithoutWidth(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	reason := strings.Repeat("long ", 40)
	metadata := hookMetadata(t, map[string]any{"name": "guard", "decision": "deny", "reason": reason})

	out := ansi.Strip(toolOutputHookIndicator(&sty, metadata, 0, false))
	require.Contains(t, out, "Hook guard → Denied "+reason)
}

// A drill-in keeps both the hooks and a tool's output when the output
// starts without a header, as an error for unreadable input does.
func TestToolDetailOutputKeepsHooksOverHeaderlessOutput(t *testing.T) {
	t.Parallel()

	metadata := hookMetadata(t, map[string]any{"name": "guard", "decision": "allow"})
	result := &message.ToolResult{ToolCallID: "tc-test", Name: "job_output", Content: "x", Metadata: metadata}
	source := helperToolItem(t, "job_output", "not json", result, ToolStatusSuccess)

	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source, nil)
	lines := strings.Split(ansi.Strip(items[len(items)-1].Render(80)), "\n")
	require.Len(t, lines, 3, "hooks, a blank line, then the output: %q", lines)
	require.Contains(t, lines[0], "Hook guard")
	require.Empty(t, strings.TrimSpace(lines[1]))
	require.Contains(t, lines[2], "Invalid parameters")
}

// A lone carriage return in code doesn't start a numbered line, so the
// numbers stay the file's own.
func TestToolOutputCodeContentKeepsFileLines(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	out := ansi.Strip(toolOutputCodeContent(&sty, "a.go", "package a\rfunc x() {}\nvar y = 1", 0, 60, true, true))
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 2, "%q", out)
	require.Contains(t, lines[0], "package a func x() {}")
	require.NotContains(t, out, "\r")
}

// Markdown output breaks at a carriage return instead of sending one.
func TestToolOutputMarkdownContentBreaksCarriageReturns(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	out := toolOutputMarkdownContent(&sty, "first\rsecond\n\nthird", 60, true, true)
	require.NotContains(t, out, "\r")
	require.Contains(t, ansi.Strip(out), "second")
}

// A drill-in's error wraps at a fixed margin beside its tag, with no blank
// lines from the ends of the message.
func TestToolResultErrorContentWrapsAtMargin(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	text := "\n\n" + strings.Repeat("boom ", 30) + "\n\n"
	out := toolResultErrorContent(&sty, &message.ToolResult{Content: text}, 60, true)
	lines := strings.Split(out, "\n")
	require.Greater(t, len(lines), 1)
	for i, line := range lines {
		require.Equal(t, 58, lipgloss.Width(line), "line %d: %q", i, ansi.Strip(line))
		require.NotEmpty(t, strings.TrimSpace(ansi.Strip(line)), "line %d is blank", i)
	}
}

// Edits and writes that fail show their whole error in the drill-in, even
// when they carry a diff.
func TestToolDetailOutputWrapsFileErrors(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("could not write ", 10) + "END"
	metadata := mustJSON(t, map[string]any{
		"old_content": "a\n", "new_content": "b\n",
		"diff": "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b",
	})
	input := mustJSON(t, map[string]any{
		"file_path": "/tmp/f", "content": "b", "old_string": "a", "new_string": "b",
		"edits": []any{map[string]any{"old_string": "a", "new_string": "b"}},
	})
	for _, name := range []string{"edit", "multiedit", "write"} {
		result := &message.ToolResult{ToolCallID: "tc-test", Name: name, Content: text, IsError: true, Metadata: metadata}
		source := helperToolItem(t, name, input, result, ToolStatusError)

		sty := styles.TokyoNight()
		items := BuildToolDetailItems(&sty, source, nil)
		require.Contains(t, squash(items[len(items)-1].Render(60)), squash(text), name)
	}
}

// The chat holds a denial reason to the hook's one line.
func TestHookReasonStaysOnOneLine(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	metadata := hookMetadata(t, map[string]any{"name": "g", "decision": "deny", "reason": "not\nhere"})
	out := ansi.Strip(toolOutputHookIndicator(&sty, metadata, 80, false))
	require.NotContains(t, out, "\n")
	require.Contains(t, out, "Denied not here")
}

// A drill-in lays hooks out at the width its tool's output uses, and ends
// with them when there's no output.
func TestToolDetailOutputHookLayout(t *testing.T) {
	t.Parallel()

	reason := strings.Repeat("blocked ", 40)
	metadata := hookMetadata(t, map[string]any{"name": "guard", "decision": "deny", "reason": reason})
	sty := styles.TokyoNight()

	// Fetch caps its width, so on a wide screen its hooks wrap at the cap.
	fetch := helperToolItem(t, "fetch", `{"url":"https://example.com"}`,
		&message.ToolResult{ToolCallID: "tc-test", Name: "fetch", Content: "body", Metadata: metadata}, ToolStatusSuccess)
	items := BuildToolDetailItems(&sty, fetch, nil)
	for _, line := range strings.Split(items[len(items)-1].Render(300), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), MessageLeftPaddingTotal+cappedMessageWidth(300))
	}

	empty := helperToolItem(t, "unknown_tool", `{}`,
		&message.ToolResult{ToolCallID: "tc-test", Name: "unknown_tool", Metadata: metadata}, ToolStatusSuccess)
	items = BuildToolDetailItems(&sty, empty, nil)
	lines := strings.Split(ansi.Strip(items[len(items)-1].Render(80)), "\n")
	require.NotEmpty(t, strings.TrimSpace(lines[len(lines)-1]), "no blank line after the hooks: %q", lines)
}

// In the chat a hook's name gives way for its matcher before the matcher
// gives way for the reason.
func TestHookLineNameGivesWayForMatcher(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	reason := strings.Repeat("r", 40)
	metadata := hookMetadata(t, map[string]any{
		"name": strings.Repeat("n", 30), "matcher": "Bash", "decision": "deny", "reason": reason,
	})
	out := ansi.Strip(toolOutputHookIndicator(&sty, metadata, 80, false))
	require.Equal(t, 80, lipgloss.Width(out), "%q", out)
	require.Contains(t, out, " Bash → Denied "+reason)
}

// A permission denial is tagged WARN even when its message breaks a line.
func TestToolErrorContentWarnsOnDenialAcrossLines(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	out := ansi.Strip(toolErrorContent(&sty, &message.ToolResult{Content: "User denied\npermission"}, 60))
	require.True(t, strings.HasPrefix(strings.TrimSpace(out), "WARN"), "%q", out)
}

// A hook name's breaks and tabs show as pilcrows and spaces on one line.
func TestHookNameFlattensWhitespace(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	metadata := hookMetadata(t, map[string]any{"name": "a\r\nb\nc\rd\ve\ff\tg", "decision": "allow"})
	require.Equal(t, "Hook a¶b¶c¶d¶e¶f    g → OK", ansi.Strip(toolOutputHookIndicator(&sty, metadata, 80, false)))
}

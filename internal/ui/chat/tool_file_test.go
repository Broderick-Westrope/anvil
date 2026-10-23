package chat

import (
	"runtime"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func pressKey(item MessageItem, k string) (bool, tea.Msg) {
	handled, cmd := item.(KeyEventHandler).HandleKeyEvent(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
	if cmd == nil {
		return handled, nil
	}
	return handled, cmd()
}

func TestToolDrillInOpensTheFileItsCallNamesFromEveryItem(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "edit", `{"file_path":"/tmp/a.go","old_string":"a","new_string":"b"}`, &message.ToolResult{Content: "ok"}, ToolStatusSuccess)
	items := BuildToolDetailItems(&sty, source, &fakeImages{})

	for _, item := range items {
		open, edit := FileKeys(item)
		require.Equal(t, OpensFile, open, item.ID())
		require.True(t, edit, item.ID())
		handled, msg := pressKey(item, "o")
		require.True(t, handled, item.ID())
		require.Equal(t, FileOpenMsg{Path: "/tmp/a.go"}, msg, item.ID())
		handled, msg = pressKey(item, "e")
		require.True(t, handled, item.ID())
		require.Equal(t, FileEditMsg{Path: "/tmp/a.go"}, msg, item.ID())
		handled, msg = pressKey(item, "r")
		require.True(t, handled, item.ID())
		require.Equal(t, FileRevealMsg{Path: "/tmp/a.go"}, msg, item.ID())
		handled, msg = pressKey(item, "x")
		require.False(t, handled, item.ID())
		require.Nil(t, msg, item.ID())
	}
}

func TestToolDrillInWhileAwaitingPermissionOpensTheFile(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "write", `{"file_path":"a.go","content":"x"}`, nil, ToolStatusAwaitingPermission)
	items := BuildToolDetailItems(&sty, source, nil)

	handled, msg := pressKey(items[len(items)-1], "o")
	require.True(t, handled)
	require.Equal(t, FileOpenMsg{Path: "a.go"}, msg)
}

func TestToolFileLine(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		tool  string
		input string
		line  int
	}{
		"view without an offset starts at the top": {tool: "view", input: `{"file_path":"/a"}`, line: 0},
		"view's 0-based offset":                    {tool: "view", input: `{"file_path":"/a","offset":0}`, line: 1},
		"view from line 41":                        {tool: "view", input: `{"file_path":"/a","offset":40}`, line: 41},
		"a negative offset is ignored":             {tool: "view", input: `{"file_path":"/a","offset":-3}`, line: 0},
		"a malformed offset keeps the path":        {tool: "view", input: `{"file_path":"/a","offset":"x"}`, line: 0},
		"other tools' offsets aren't lines":        {tool: "lsp_symbols", input: `{"file_path":"/a","offset":40}`, line: 0},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sty := styles.TokyoNight()
			source := helperToolItem(t, tc.tool, tc.input, &message.ToolResult{Content: "x"}, ToolStatusSuccess)
			items := BuildToolDetailItems(&sty, source, nil)

			_, msg := pressKey(items[0], "e")
			require.Equal(t, FileEditMsg{Path: "/a", Line: tc.line}, msg)
		})
	}
}

func TestToolDrillInWithoutAFileLeavesTheKeys(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		tool  string
		input string
	}{
		"no file_path":    {tool: "bash", input: `{"command":"ls"}`},
		"an empty path":   {tool: "view", input: `{"file_path":""}`},
		"a skill":         {tool: "view", input: `{"file_path":"anvil://skills/jq/SKILL.md"}`},
		"a non-string":    {tool: "view", input: `{"file_path":3}`},
		"malformed input": {tool: "view", input: `{"file_path":`},
		"no input":        {tool: "view", input: ``},
		"an MCP tool":     {tool: "mcp_github_get_file", input: `{"file_path":"README.md"}`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sty := styles.TokyoNight()
			source := helperToolItem(t, tc.tool, tc.input, &message.ToolResult{Content: "x"}, ToolStatusSuccess)
			for _, item := range BuildToolDetailItems(&sty, source, &fakeImages{}) {
				open, edit := FileKeys(item)
				require.Equal(t, OpensNothing, open, item.ID())
				require.False(t, edit, item.ID())
				for _, k := range []string{"o", "e", "r"} {
					handled, msg := pressKey(item, k)
					require.False(t, handled, item.ID())
					require.Nil(t, msg, item.ID())
				}
				require.NotContains(t, ansi.Strip(item.Render(80)), "open in $EDITOR", item.ID())
			}
		})
	}
}

func TestFileKeysAreOffOutsideADrillIn(t *testing.T) {
	t.Parallel()

	open, edit := FileKeys(helperToolItem(t, "view", `{"file_path":"/a"}`, nil, ToolStatusSuccess))
	require.Equal(t, OpensNothing, open)
	require.False(t, edit)
}

func TestToolDetailOutputImageKeepsOForTheImageAndEForTheFile(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/tmp/a.png"}`, imageResult(), ToolStatusSuccess)
	out := outputItem(t, BuildToolDetailItems(&sty, source, &fakeImages{}))
	out.Render(80)

	_, msg := pressKey(out, "o")
	require.Equal(t, ImageOpenMsg{Data: "aGVsbG8="}, msg)
	_, msg = pressKey(out, "e")
	require.Equal(t, FileEditMsg{Path: "/tmp/a.png"}, msg)
}

func TestToolDetailHeaderHintsTheFileKeys(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/a"}`, &message.ToolResult{Content: "x"}, ToolStatusSuccess)
	header := BuildToolDetailItems(&sty, source, nil)[0]

	out := ansi.Strip(header.Render(80))
	require.Contains(t, out, "View  o open file  e open in $EDITOR")
}

func TestToolDetailHeaderHintAppearsOnceTheInputNamesAFile(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_pa`, nil, ToolStatusRunning)
	header := BuildToolDetailItems(&sty, source, nil)[0]
	require.NotContains(t, ansi.Strip(header.Render(80)), "open file")

	tc := source.ToolCall()
	tc.Input = `{"file_path":"/a"}`
	source.SetToolCall(tc)

	require.Contains(t, ansi.Strip(header.Render(80)), "open file")
}

func TestEveryBuiltInFileToolOpensItsFile(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"view", "edit", "multiedit", "write", "download", "lsp_symbols", "lsp_diagnostics", "lsp_rename"} {
		sty := styles.TokyoNight()
		source := helperToolItem(t, name, `{"file_path":"/a"}`, &message.ToolResult{Content: "x"}, ToolStatusSuccess)
		open, edit := FileKeys(BuildToolDetailItems(&sty, source, nil)[0])
		require.Equal(t, OpensFile, open, name)
		require.True(t, edit, name)
	}
}

func TestToolFileFollowsTheInputAsItChanges(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/a"}`, nil, ToolStatusRunning)
	header := BuildToolDetailItems(&sty, source, nil)[0]
	_, msg := pressKey(header, "o")
	require.Equal(t, FileOpenMsg{Path: "/a"}, msg)

	tc := source.ToolCall()
	tc.Input = `{"file_path":"/b"}`
	source.SetToolCall(tc)
	_, msg = pressKey(header, "o")
	require.Equal(t, FileOpenMsg{Path: "/b"}, msg)
}

func TestToolDetailHeaderHintsWhatOOpensOnAnImage(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/tmp/a.png"}`, imageResult(), ToolStatusSuccess)

	header := ansi.Strip(BuildToolDetailItems(&sty, source, &fakeImages{})[0].Render(80))
	require.Contains(t, header, "View  o open in viewer  e open in $EDITOR")

	header = ansi.Strip(BuildToolDetailItems(&sty, source, nil)[0].Render(80))
	require.Contains(t, header, "o open file", "nothing draws the image, so o opens the file")
}

func TestToolDetailHeaderDropsTheHintWhenItDoesntFit(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/a"}`, &message.ToolResult{Content: "x"}, ToolStatusSuccess)
	header := BuildToolDetailItems(&sty, source, nil)[0]

	full := header.Render(200)
	fits := ansi.StringWidth(full)
	require.Contains(t, ansi.Strip(header.Render(fits)), "open in $EDITOR", "exactly as wide as the hint")
	narrow := header.Render(fits - 1)
	require.NotContains(t, ansi.Strip(narrow), "open")
	require.Contains(t, ansi.Strip(narrow), "View")
}

func TestAnImageResultKeepsOForTheImageThroughoutTheDrillIn(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/tmp/a.png"}`, imageResult(), ToolStatusSuccess)
	items := BuildToolDetailItems(&sty, source, &fakeImages{})

	for _, item := range items {
		item.Render(80)
		open, edit := FileKeys(item)
		require.Equal(t, OpensImage, open, item.ID())
		require.True(t, edit, item.ID())
		_, msg := pressKey(item, "o")
		require.Equal(t, ImageOpenMsg{Data: "aGVsbG8="}, msg, item.ID())
		_, msg = pressKey(item, "e")
		require.Equal(t, FileEditMsg{Path: "/tmp/a.png"}, msg, item.ID())
	}
}

func TestAnMCPImageOpensFromEveryItem(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "mcp_shots_take", `{"url":"x"}`, imageResult(), ToolStatusSuccess)
	header := BuildToolDetailItems(&sty, source, &fakeImages{})[0]

	open, edit := FileKeys(header)
	require.Equal(t, OpensImage, open)
	require.False(t, edit)
	require.Contains(t, ansi.Strip(header.Render(80)), "o open in viewer")
	handled, msg := pressKey(header, "e")
	require.False(t, handled)
	require.Nil(t, msg)
}

func TestToolDetailHeaderFollowsEachKeyAsItChanges(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()

	// An image arriving changes only what o opens.
	source := helperToolItem(t, "view", `{"file_path":"/a.png"}`, nil, ToolStatusRunning)
	header := BuildToolDetailItems(&sty, source, &fakeImages{})[0]
	require.Contains(t, ansi.Strip(header.Render(80)), "o open file  e open in $EDITOR")
	source.SetResult(imageResult())
	require.Contains(t, ansi.Strip(header.Render(80)), "o open in viewer  e open in $EDITOR")

	// A path arriving on an image call changes only e.
	source = helperToolItem(t, "view", `{"file_pa`, imageResult(), ToolStatusRunning)
	header = BuildToolDetailItems(&sty, source, &fakeImages{})[0]
	require.NotContains(t, ansi.Strip(header.Render(80)), "$EDITOR")
	tc := source.ToolCall()
	tc.Input = `{"file_path":"/a.png"}`
	source.SetToolCall(tc)
	require.Contains(t, ansi.Strip(header.Render(80)), "e open in $EDITOR")
}

func TestToolDetailHeaderFollowsTheStatus(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/a"}`, nil, ToolStatusRunning)
	header := BuildToolDetailItems(&sty, source, nil)[0]
	running := header.Render(80)

	source.SetStatus(ToolStatusError)

	require.NotEqual(t, running, header.Render(80))
}

func TestRevealLabelNamesTheFileManager(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"darwin":  "reveal in Finder",
		"windows": "show in Explorer",
		"linux":   "show in file manager",
		"freebsd": "show in file manager",
	}
	for goos, want := range tests {
		require.Equal(t, want, RevealLabel(goos), goos)
	}
}

func TestToolDetailHeaderHintsR(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/a"}`, &message.ToolResult{Content: "x"}, ToolStatusSuccess)
	header := ansi.Strip(BuildToolDetailItems(&sty, source, nil)[0].Render(200))
	require.Contains(t, header, "e open in $EDITOR  r "+RevealLabel(runtime.GOOS))
}

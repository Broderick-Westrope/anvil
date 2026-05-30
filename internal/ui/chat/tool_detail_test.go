package chat

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// helperToolItem creates a minimal ToolMessageItem for testing detail views.
func helperToolItem(t *testing.T, name, input string, result *message.ToolResult, status ToolStatus) ToolMessageItem {
	t.Helper()
	sty := styles.TokyoNight()
	tc := message.ToolCall{
		ID:       "tc-test",
		Name:     name,
		Input:    input,
		Finished: status != ToolStatusRunning,
	}
	item := NewToolMessageItem(&sty, "msg-test", tc, result, false, nil)
	item.SetStatus(status)
	return item
}

func TestBuildToolDetailItems_Structure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		toolName   string
		input      string
		result     *message.ToolResult
		status     ToolStatus
		wantCounts map[string]int // type name → expected count
		wantTotal  int
	}{
		{
			name:     "tool with params and result",
			toolName: "bash",
			input:    `{"command":"echo hello","description":"run echo"}`,
			result:   &message.ToolResult{Content: "hello"},
			status:   ToolStatusSuccess,
			wantCounts: map[string]int{
				"header":  1,
				"section": 2, // Input + Output
				"param":   2, // command + description
				"output":  1,
			},
			wantTotal: 6,
		},
		{
			name:     "tool with no params",
			toolName: "bash",
			input:    `{}`,
			result:   &message.ToolResult{Content: "done"},
			status:   ToolStatusSuccess,
			wantCounts: map[string]int{
				"header":  1,
				"section": 2,
				"output":  1,
			},
			wantTotal: 4,
		},
		{
			name:     "tool with empty input",
			toolName: "view",
			input:    "",
			result:   &message.ToolResult{Content: "file content"},
			status:   ToolStatusSuccess,
			wantCounts: map[string]int{
				"header":  1,
				"section": 2,
				"static":  1, // "(no input)"
				"output":  1,
			},
			wantTotal: 5,
		},
		{
			name:     "tool awaiting permission",
			toolName: "bash",
			input:    `{"command":"rm -rf /"}`,
			result:   nil,
			status:   ToolStatusAwaitingPermission,
			wantCounts: map[string]int{
				"header":  1,
				"section": 2,
				"param":   1,
				"static":  1, // "Awaiting permission..."
			},
			wantTotal: 5,
		},
		{
			name:     "tool with invalid JSON input",
			toolName: "bash",
			input:    `not json`,
			result:   &message.ToolResult{Content: "error"},
			status:   ToolStatusError,
			wantCounts: map[string]int{
				"header":  1,
				"section": 2,
				"static":  1, // raw input fallback
				"output":  1,
			},
			wantTotal: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := helperToolItem(t, tt.toolName, tt.input, tt.result, tt.status)
			sty := styles.TokyoNight()
			items := BuildToolDetailItems(&sty, source)

			require.Len(t, items, tt.wantTotal, "total item count")

			counts := map[string]int{}
			for _, item := range items {
				switch item.(type) {
				case *toolDetailHeaderItem:
					counts["header"]++
				case *toolDetailSectionItem:
					counts["section"]++
				case *toolDetailParamItem:
					counts["param"]++
				case *toolDetailOutputItem:
					counts["output"]++
				case *toolDetailStaticItem:
					counts["static"]++
				}
			}
			for typ, want := range tt.wantCounts {
				require.Equal(t, want, counts[typ], "count of %s items", typ)
			}
		})
	}
}

func TestBuildToolDetailItems_ParamsSortedByKey(t *testing.T) {
	t.Parallel()
	source := helperToolItem(t, "bash", `{"zebra":"z","alpha":"a","mid":"m"}`, &message.ToolResult{Content: "ok"}, ToolStatusSuccess)
	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source)

	var paramKeys []string
	for _, item := range items {
		if p, ok := item.(*toolDetailParamItem); ok {
			paramKeys = append(paramKeys, p.key)
		}
	}
	require.Equal(t, []string{"alpha", "mid", "zebra"}, paramKeys)
}

func TestBuildToolDetailItems_AllItemsRenderable(t *testing.T) {
	t.Parallel()
	source := helperToolItem(t, "edit", `{"file_path":"main.go","old_string":"a","new_string":"b"}`, &message.ToolResult{Content: "edited"}, ToolStatusSuccess)
	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source)

	for _, item := range items {
		rendered := item.Render(80)
		require.NotEmpty(t, rendered, "item %T should produce non-empty render", item)
	}
}

func TestBuildToolDetailItems_UniqueIDs(t *testing.T) {
	t.Parallel()
	source := helperToolItem(t, "bash", `{"command":"ls","description":"list files"}`, &message.ToolResult{Content: "ok"}, ToolStatusSuccess)
	sty := styles.TokyoNight()
	items := BuildToolDetailItems(&sty, source)

	ids := make(map[string]struct{})
	for _, item := range items {
		id := item.ID()
		require.NotEmpty(t, id)
		_, dup := ids[id]
		require.False(t, dup, "duplicate ID: %s", id)
		ids[id] = struct{}{}
	}
}

func TestToolDetailParamItem_ToggleExpanded(t *testing.T) {
	t.Parallel()

	t.Run("single line value does not toggle", func(t *testing.T) {
		t.Parallel()
		sty := styles.TokyoNight()
		p := &toolDetailParamItem{Versioned: list.NewVersioned(),
			sty:   &sty,
			key:   "file_path",
			value: "/tmp/test.go",
		}
		expanded := p.ToggleExpanded()
		require.False(t, expanded, "single-line param should not toggle")
	})

	t.Run("multi-line value toggles", func(t *testing.T) {
		t.Parallel()
		sty := styles.TokyoNight()
		p := &toolDetailParamItem{Versioned: list.NewVersioned(),
			sty:   &sty,
			key:   "content",
			value: "line1\nline2\nline3",
		}
		require.False(t, p.expanded)
		expanded := p.ToggleExpanded()
		require.True(t, expanded)
		expanded = p.ToggleExpanded()
		require.False(t, expanded)
	})

	t.Run("non-string value does not toggle", func(t *testing.T) {
		t.Parallel()
		sty := styles.TokyoNight()
		p := &toolDetailParamItem{Versioned: list.NewVersioned(),
			sty:   &sty,
			key:   "limit",
			value: float64(20),
		}
		expanded := p.ToggleExpanded()
		require.False(t, expanded, "non-string param should not toggle")
	})
}

func TestToolDetailOutputItem_ToggleExpanded(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	source := helperToolItem(t, "bash", `{"command":"echo hi"}`, &message.ToolResult{Content: "hi"}, ToolStatusSuccess)
	o := &toolDetailOutputItem{Versioned: list.NewVersioned(),
		sty:    &sty,
		source: source,
	}
	require.False(t, o.expanded)
	expanded := o.ToggleExpanded()
	require.True(t, expanded)
	expanded = o.ToggleExpanded()
	require.False(t, expanded)
}

func TestToolDetailItems_CacheInvalidation(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	source := helperToolItem(t, "bash", `{"command":"echo hi"}`, &message.ToolResult{Content: "hi"}, ToolStatusSuccess)
	items := BuildToolDetailItems(&sty, source)

	const width = 80

	for _, item := range items {
		first := item.Render(width)
		second := item.Render(width)
		require.Equal(t, first, second, "same-width render should use cache for %T", item)
	}
}

func TestToolDetailItems_WidthSensitive(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	source := helperToolItem(t, "bash", `{"command":"echo hi"}`, &message.ToolResult{Content: "hi"}, ToolStatusSuccess)
	items := BuildToolDetailItems(&sty, source)

	// Section dividers pad with "─" to fill width, so they're always
	// width-sensitive.
	for _, item := range items {
		if _, ok := item.(*toolDetailSectionItem); ok {
			narrow := item.Render(40)
			wide := item.Render(120)
			require.NotEqual(t, narrow, wide,
				"width change should produce different output for section divider")
		}
	}
}

func TestToolDetailItems_FocusChangesRender(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	source := helperToolItem(t, "bash", `{"command":"echo hi"}`, &message.ToolResult{Content: "hi"}, ToolStatusSuccess)
	items := BuildToolDetailItems(&sty, source)

	const width = 80

	for _, item := range items {
		focusable, ok := item.(list.Focusable)
		if !ok {
			continue
		}
		focusable.SetFocused(false)
		blurred := item.Render(width)

		focusable.SetFocused(true)
		focused := item.Render(width)

		require.NotEqual(t, blurred, focused,
			"focus state should change render for %T", item)
	}
}

func TestToolDetailItems_AllFinished(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	source := helperToolItem(t, "bash", `{"command":"echo hi"}`, &message.ToolResult{Content: "hi"}, ToolStatusSuccess)
	items := BuildToolDetailItems(&sty, source)

	for _, item := range items {
		require.True(t, item.Finished(), "%T should be Finished", item)
	}
}

func TestStripFirstLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"multi-line", "header\nbody\nmore", "body\nmore"},
		{"single line", "only", "only"},
		{"empty", "", ""},
		{"just newline", "\n", ""},
		{"header then empty", "header\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, stripFirstLine(tt.input))
		})
	}
}

func TestSectionHeader(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	result := sectionHeader(&sty, "Input", 40)
	require.NotEmpty(t, result)
	require.True(t, strings.Contains(result, "Input"),
		"section header should contain label")
}

func TestToolDetailParamItem_IsMultiLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   any
		wantStr string
		wantOK  bool
	}{
		{"single line string", "hello", "hello", false},
		{"multi-line string", "a\nb", "a\nb", true},
		{"non-string", float64(42), "", false},
		{"nil value", nil, "", false},
		{"empty string", "", "", false},
	}

	sty := styles.TokyoNight()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &toolDetailParamItem{Versioned: list.NewVersioned(), sty: &sty, key: "k", value: tt.value}
			gotStr, gotOK := p.isMultiLine()
			require.Equal(t, tt.wantStr, gotStr)
			require.Equal(t, tt.wantOK, gotOK)
		})
	}
}

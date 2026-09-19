package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/fsext"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/anim"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestViewSkillPending(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input string
		wantName    bool
	}{
		{"name", `{"skill_name":"euc-go","limit":10,"offset":2}`, true},
		{"partial", `{"skill_na`, false},
		{"path", `{"file_path":"main.go"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sty := styles.TokyoNight()
			animation := anim.New(anim.Settings{ID: "view-test", Size: 5})
			opts := &ToolRenderOpts{ToolCall: message.ToolCall{Input: tc.input}, Anim: animation}
			got := (&ViewToolRenderContext{}).RenderTool(&sty, 120, opts)
			if tc.wantName {
				require.Contains(t, ansi.Strip(got), "View euc-go")
				require.NotContains(t, ansi.Strip(got), "limit")
				require.NotContains(t, ansi.Strip(got), "offset")
				require.Contains(t, got, animation.Render())
			} else {
				require.Equal(t, pendingTool(&sty, "View", animation, false), got)
			}
		})
	}
}

func TestViewSkillResolvedLocation(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	disk := filepath.Join(home, "skills", "euc-go", "SKILL.md")
	diskJSON, err := json.Marshal(disk)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, metadata, location string
	}{
		{"disk", `{"resource_type":"skill","resource_name":"euc-go","file_path":` + string(diskJSON) + `}`, fsext.PrettyPath(disk)},
		{"builtin", `{"resource_type":"skill","resource_name":"jq","file_path":"anvil://skills/jq/SKILL.md"}`, "anvil://skills/jq/SKILL.md"},
		{"empty", "", ""},
		{"invalid", "{", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sty := styles.TokyoNight()
			opts := &ToolRenderOpts{
				ToolCall: message.ToolCall{Input: `{"skill_name":"euc-go"}`, Finished: true},
				Status:   ToolStatusSuccess,
				Result:   &message.ToolResult{Metadata: tc.metadata},
			}
			got := ansi.Strip((&ViewToolRenderContext{}).RenderTool(&sty, 160, opts))
			require.Contains(t, got, "View euc-go")
			if tc.location != "" {
				require.Contains(t, got, "location="+tc.location)
				require.Contains(t, got, "Loaded Skill")
			} else {
				require.NotContains(t, got, "location=")
				require.NotContains(t, got, "()")
			}
		})
	}
}

func TestViewSkillPendingToSuccess(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	call := message.ToolCall{ID: "view-skill", Name: tools.ViewToolName, Input: `{"skill_name":"euc-go"}`}
	item := NewViewToolMessageItem(&sty, call, nil, false)
	pending := ansi.Strip(item.Render(160))
	require.Contains(t, pending, "euc-go")
	require.NotContains(t, pending, "location=")
	call.Finished = true
	item.SetToolCall(call)
	item.SetResult(&message.ToolResult{ToolCallID: call.ID, Metadata: `{"resource_type":"skill","resource_name":"euc-go","file_path":"/skills/euc-go/SKILL.md"}`})
	completed := ansi.Strip(item.Render(160))
	require.Contains(t, completed, "View euc-go (location=/skills/euc-go/SKILL.md)")
	require.Contains(t, completed, "Loaded Skill")
}

func TestViewSkillCopy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, want string }{
		{"name", `{"skill_name":"euc-go"}`, "**Skill:** euc-go"},
		{"path", `{"file_path":"main.go","limit":10,"offset":2}`, "**File:** main.go\n**Limit:** 10\n**Offset:** 2"},
		{"neither", `{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := &baseToolMessageItem{toolCall: message.ToolCall{Name: tools.ViewToolName, Input: tc.input}}
			require.Equal(t, tc.want, item.formatParametersForCopy())
		})
	}
}

func TestViewSkillDetailFormatting(t *testing.T) {
	t.Parallel()
	const content = "---\nname: example\ndescription: A skill description that wraps across multiple lines in a narrow terminal.\n---\n\n# Example\n\nFirst paragraph.\n\n## Steps\n\n- First step\n- Last step"
	for _, tc := range []struct {
		name, input, path string
	}{
		{"name", `{"skill_name":"example"}`, "/skills/example/SKILL.md"},
		{"path", `{"file_path":"/skills/example/SKILL.md"}`, "/skills/example/SKILL.md"},
		{"builtin", `{"skill_name":"example"}`, "anvil://skills/example/SKILL.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sty := styles.TokyoNight()
			metadata, err := json.Marshal(tools.ViewResponseMetadata{
				FilePath:            tc.path,
				Content:             content,
				ResourceType:        tools.ViewResourceSkill,
				ResourceName:        "example",
				ResourceDescription: "A skill description that wraps across multiple lines in a narrow terminal.",
			})
			require.NoError(t, err)
			call := message.ToolCall{ID: "view-skill-detail", Name: tools.ViewToolName, Input: tc.input, Finished: true}
			source := NewViewToolMessageItem(&sty, call, &message.ToolResult{Metadata: string(metadata), Content: "wrapped response"}, false)
			items := BuildToolDetailItems(&sty, source)
			output := items[len(items)-1]

			fileCall := message.ToolCall{ID: "view-file-detail", Name: tools.ViewToolName, Input: `{"file_path":"SKILL.md"}`, Finished: true}
			fileSource := NewViewToolMessageItem(&sty, fileCall, &message.ToolResult{Content: content}, false)
			fileItems := BuildToolDetailItems(&sty, fileSource)
			fileOutput := fileItems[len(fileItems)-1]

			for _, width := range []int{40, 120} {
				require.Equal(t, fileOutput.Render(width), output.Render(width))
				require.Contains(t, ansi.Strip(output.Render(width)), "description:")
				require.NotContains(t, ansi.Strip(output.Render(width)), "Last step")
			}

			require.True(t, output.(Expandable).ToggleExpanded())
			require.True(t, fileOutput.(Expandable).ToggleExpanded())
			for _, width := range []int{40, 120} {
				require.Equal(t, fileOutput.Render(width), output.Render(width))
				require.Contains(t, ansi.Strip(output.Render(width)), "Last step")
			}
		})
	}
}

func TestViewPathBaseline(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	const header = "\x1b[38;2;158;206;106m✓\x1b[m \x1b[38;2;122;162;247mView\x1b[m \x1b[38;2;59;66;97mmain.go (limit=10, offset=2)\x1b[m"
	opts := &ToolRenderOpts{
		ToolCall: message.ToolCall{Input: `{"file_path":"main.go","limit":10,"offset":2}`, Finished: true},
		Status:   ToolStatusSuccess,
	}
	require.Equal(t, header, (&ViewToolRenderContext{}).RenderTool(&sty, 120, opts))
}

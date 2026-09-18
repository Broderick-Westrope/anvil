package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestViewSkillStateMatrix(t *testing.T) {
	t.Parallel()
	const (
		pathParams     = " \x1b[38;2;122;162;247mView\x1b[m \x1b[38;2;59;66;97mmain.go (limit=10, offset=2)\x1b[m"
		pathPending    = "\x1b[38;2;65;166;181m●\x1b[m \x1b[38;2;122;162;247mView\x1b[m "
		pathRunning    = "\x1b[38;2;65;166;181m●\x1b[m" + pathParams + "\n\n\x1b[38;2;59;66;97mWaiting for tool response...\x1b[m"
		pathPermission = "\x1b[38;2;65;166;181m●\x1b[m" + pathParams + "\n\n\x1b[38;2;59;66;97mRequesting permission...\x1b[m"
		pathCanceled   = "\x1b[38;2;169;177;214m●\x1b[m" + pathParams + "\n\n\x1b[38;2;59;66;97mCanceled.\x1b[m"
		pathError      = "\x1b[38;2;219;75;75m×\x1b[m" + pathParams + "\n\n\x1b[48;2;247;118;142m \x1b[m\x1b[38;2;26;27;38;48;2;247;118;142mERROR\x1b[m\x1b[48;2;247;118;142m \x1b[m \x1b[38;2;86;95;137mread failed\x1b[m"
		pathSuccess    = "\x1b[38;2;158;206;106m✓\x1b[m" + pathParams
	)
	for _, state := range []struct {
		name     string
		status   ToolStatus
		pending  bool
		wantPath string
	}{
		{"pending", ToolStatusRunning, true, pathPending},
		{"running", ToolStatusRunning, false, pathRunning},
		{"permission", ToolStatusAwaitingPermission, false, pathPermission},
		{"canceled", ToolStatusCanceled, false, pathCanceled},
		{"error", ToolStatusError, false, pathError},
		{"success", ToolStatusSuccess, false, pathSuccess},
	} {
		for _, selector := range []string{"skill_name", "file_path"} {
			for _, layout := range []string{"normal", "compact", "expanded"} {
				t.Run(state.name+"/"+selector+"/"+layout, func(t *testing.T) {
					t.Parallel()
					sty := styles.TokyoNight()
					input := `{"skill_name":"euc-go"}`
					if selector == "file_path" {
						input = `{"file_path":"main.go","limit":10,"offset":2}`
					}
					opts := &ToolRenderOpts{
						ToolCall:        message.ToolCall{Input: input, Finished: !state.pending},
						Status:          state.status,
						Compact:         layout == "compact",
						ExpandedContent: layout == "expanded",
					}
					if state.status == ToolStatusError {
						opts.Result = &message.ToolResult{Content: "read failed", IsError: true}
					}
					if state.status == ToolStatusSuccess {
						opts.Result = &message.ToolResult{}
						if selector == "skill_name" {
							opts.Result.Metadata = `{"resource_type":"skill","resource_name":"euc-go","resource_description":"Go conventions","file_path":"/skills/euc-go/SKILL.md"}`
						}
					}
					got := (&ViewToolRenderContext{}).RenderTool(&sty, 120, opts)
					plain := ansi.Strip(got)
					header, _, _ := strings.Cut(plain, "\n")
					require.Equal(t, 1, strings.Count(plain, "View"))
					if selector == "file_path" {
						want := state.wantPath
						if layout == "compact" {
							want, _, _ = strings.Cut(want, "\n")
							want = strings.Replace(want, sty.Tool.NameNormal.Render("View"), sty.Tool.NameNested.Render("View"), 1)
						}
						require.Equal(t, want, got)
					} else {
						require.Contains(t, header, "View euc-go")
						require.NotContains(t, header, "<nil>")
						require.NotContains(t, header, "  ")
						require.NotContains(t, header, "()")
						require.NotContains(t, header, "location=)")
						require.False(t, strings.HasSuffix(header, "="))
						require.False(t, strings.HasSuffix(header, ","))
						require.Equal(t, strings.TrimSpace(header), header)
						if state.status == ToolStatusSuccess {
							require.Contains(t, header, "location=/skills/euc-go/SKILL.md")
						} else {
							require.NotContains(t, header, "location=")
						}
						if layout == "compact" {
							require.Contains(t, got, sty.Tool.NameNested.Render("View"))
							require.NotContains(t, plain, "\n")
						}
					}
				})
			}
		}
	}
}

func TestViewSkillWidthMatrix(t *testing.T) {
	t.Parallel()
	const location = "/opt/organization/shared/skills/development/euc-go/SKILL.md"
	for _, width := range []int{20, 40, 120} {
		for _, layout := range []string{"normal", "compact", "expanded"} {
			t.Run(fmt.Sprintf("%d/%s", width, layout), func(t *testing.T) {
				t.Parallel()
				sty := styles.TokyoNight()
				opts := &ToolRenderOpts{
					ToolCall:        message.ToolCall{Input: `{"skill_name":"euc-go"}`, Finished: true},
					Status:          ToolStatusSuccess,
					Compact:         layout == "compact",
					ExpandedContent: layout == "expanded",
					Result:          &message.ToolResult{Metadata: `{"resource_type":"skill","resource_name":"euc-go","resource_description":"Go","file_path":"` + location + `"}`},
				}
				got := ansi.Strip((&ViewToolRenderContext{}).RenderTool(&sty, width, opts))
				header, _, _ := strings.Cut(got, "\n")
				require.Contains(t, header, "View euc-go")
				require.LessOrEqual(t, ansi.StringWidth(header), width)
				if width == 120 {
					require.Contains(t, header, "location="+location)
				} else {
					require.NotContains(t, header, "location")
					require.NotContains(t, header, "…")
				}
			})
		}
	}
}

func TestViewSkillMetadataDegradation(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	metadata, err := json.Marshal(tools.ViewResponseMetadata{
		FilePath:     filepath.Join(home, "skills/euc-go/SKILL.md"),
		ResourceType: tools.ViewResourceSkill,
		ResourceName: "euc-go",
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		result   *message.ToolResult
		location string
	}{
		{"missing result", nil, ""},
		{"empty metadata", &message.ToolResult{}, ""},
		{"invalid metadata", &message.ToolResult{Metadata: "{"}, ""},
		{"unset type", &message.ToolResult{Metadata: `{"file_path":"/skills/euc-go/SKILL.md"}`}, ""},
		{"missing location", &message.ToolResult{Metadata: `{"resource_type":"skill","resource_name":"euc-go"}`}, ""},
		{"builtin", &message.ToolResult{Metadata: `{"resource_type":"skill","resource_name":"jq","file_path":"anvil://skills/jq/SKILL.md"}`}, "anvil://skills/jq/SKILL.md"},
		{"home", &message.ToolResult{Metadata: string(metadata)}, "~/skills/euc-go/SKILL.md"},
		{"error with metadata", &message.ToolResult{Metadata: string(metadata), IsError: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sty := styles.TokyoNight()
			opts := &ToolRenderOpts{
				ToolCall: message.ToolCall{Input: `{"skill_name":"euc-go"}`, Finished: true},
				Status:   ToolStatusSuccess,
				Result:   tc.result,
			}
			got := ansi.Strip((&ViewToolRenderContext{}).RenderTool(&sty, 120, opts))
			header, _, _ := strings.Cut(got, "\n")
			if tc.location == "" {
				require.Equal(t, "✓ View euc-go", header)
			} else {
				require.Equal(t, "✓ View euc-go (location="+tc.location+")", header)
			}
		})
	}
}

func TestViewSkillSuccessBody(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	metadata, err := json.Marshal(tools.ViewResponseMetadata{
		FilePath:            "/skills/euc-go/SKILL.md",
		ResourceType:        tools.ViewResourceSkill,
		ResourceName:        "euc-go",
		ResourceDescription: "Go conventions",
		Content:             "package sentinel\nfunc hiddenBody() {}",
	})
	require.NoError(t, err)
	opts := &ToolRenderOpts{
		ToolCall: message.ToolCall{Input: `{"skill_name":"euc-go"}`, Finished: true},
		Status:   ToolStatusSuccess,
		Result:   &message.ToolResult{Metadata: string(metadata), Content: "raw skill body"},
	}
	got := ansi.Strip((&ViewToolRenderContext{}).RenderTool(&sty, 120, opts))
	require.Equal(t, "✓ View euc-go (location=/skills/euc-go/SKILL.md)\n\n  Loaded Skill → euc-go Go conventions", got)
}

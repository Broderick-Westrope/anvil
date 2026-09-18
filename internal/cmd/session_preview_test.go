package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestExtractSkillsFromNameModeMessages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, description string
	}{
		{"euc-go", "/skills/euc-go/SKILL.md", "Go conventions"},
		{"jq", "anvil://skills/jq/SKILL.md", "Query JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			metadata, err := json.Marshal(tools.ViewResponseMetadata{
				FilePath:            tc.path,
				ResourceType:        tools.ViewResourceSkill,
				ResourceName:        tc.name,
				ResourceDescription: tc.description,
			})
			require.NoError(t, err)
			input, err := json.Marshal(tools.ViewParams{SkillName: tc.name})
			require.NoError(t, err)
			const createdAt = int64(1_700_000_000)
			msgs := []*message.Message{
				{Role: message.Assistant, Parts: []message.ContentPart{
					message.ToolCall{ID: "view-skill", Name: tools.ViewToolName, Input: string(input), Finished: true},
				}},
				{Role: message.Tool, CreatedAt: createdAt, Parts: []message.ContentPart{
					message.ToolResult{ToolCallID: "view-skill", Name: tools.ViewToolName, Metadata: string(metadata)},
				}},
			}
			require.Equal(t, []sessionShowSkill{{
				Name:        tc.name,
				Description: tc.description,
				LoadedAt:    time.Unix(createdAt, 0).Format(time.RFC3339),
			}}, extractSkillsFromMessages(msgs))
			result := msgs[1].Parts[0].(message.ToolResult)
			var got tools.ViewResponseMetadata
			require.NoError(t, json.Unmarshal([]byte(result.Metadata), &got))
			require.Equal(t, tc.path, got.FilePath)
		})
	}
}

func TestRenderTailEmptyInput(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{previewNoMessages}, renderTail(nil, 80))
	require.Equal(t, []string{previewNoMessages}, renderTail([]message.Message{}, 80))
}

func TestRenderTailTextTruncationCap(t *testing.T) {
	t.Parallel()

	longText := strings.TrimSuffix(strings.Repeat("line\n", previewMaxLinesPerMessage+5), "\n")
	msgs := []message.Message{
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: longText}},
		},
	}

	got := renderTail(msgs, 80)
	// Role header + capped text lines + truncation marker.
	require.Len(t, got, 1+previewMaxLinesPerMessage+1)
	require.Equal(t, "user", got[0])
	require.Equal(t, previewTruncationMarker, got[len(got)-1])
}

func TestRenderTailTextWordWrap(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "aaaa bbbb cccc dddd"}},
		},
	}

	got := renderTail(msgs, 10)
	require.Equal(t, "user", got[0])
	require.Greater(t, len(got), 2, "expected wrapped output across multiple lines")
	for _, line := range got {
		require.LessOrEqual(t, len(line), 10)
	}
}

func TestRenderTailToolCallAndResultOneLiners(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"secret"}`},
			},
		},
		{
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "call-1", Name: "bash", Content: "12345"},
			},
		},
	}

	got := renderTail(msgs, 80)
	joined := strings.Join(got, "\n")
	require.Contains(t, joined, "→ tool: bash")
	require.Contains(t, joined, "← result: bash (5 bytes)")
	// Never render the tool payload or result body.
	require.NotContains(t, joined, "secret")
	require.NotContains(t, joined, "12345")
}

func TestRenderTailToolResultFallsBackToToolID(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "call-9", Content: "body"},
			},
		},
	}

	got := renderTail(msgs, 80)
	require.Contains(t, strings.Join(got, "\n"), "← result: call-9 (4 bytes)")
}

func TestRenderTailBinaryAndImagePlaceholders(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.BinaryContent{MIMEType: "image/png", Data: []byte{0x89, 0x50}},
				message.ImageURLContent{URL: "data:image/png;base64,AAAA"},
			},
		},
	}

	got := renderTail(msgs, 80)
	joined := strings.Join(got, "\n")
	require.Contains(t, joined, "[binary: image/png]")
	require.Contains(t, joined, "[image]")
	require.NotContains(t, joined, "base64")
	require.NotContains(t, joined, "AAAA")
}

func TestRenderTailReasoningAndFinishOmitted(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "private chain of thought"},
				message.TextContent{Text: "visible answer"},
				message.Finish{Reason: message.FinishReasonEndTurn},
			},
		},
	}

	got := renderTail(msgs, 80)
	joined := strings.Join(got, "\n")
	require.Contains(t, joined, "visible answer")
	require.NotContains(t, joined, "private chain of thought")
	require.NotContains(t, joined, string(message.FinishReasonEndTurn))
}

func TestRenderTailMetadataMessageFiltered(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{
			Role:        message.User,
			MessageType: message.MessageTypeLabel,
			Parts:       []message.ContentPart{message.LabelContent{Label: "a label"}},
		},
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
		},
	}

	got := renderTail(msgs, 80)
	joined := strings.Join(got, "\n")
	require.Contains(t, joined, "hello")
	require.NotContains(t, joined, "a label")
	// Only one role header (the metadata message renders nothing).
	require.Equal(t, 1, strings.Count(joined, "user"))
}

func TestRenderTailNoRenderablePartsNoHeader(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "only thinking"},
				message.Finish{Reason: message.FinishReasonEndTurn},
			},
		},
	}

	require.Equal(t, []string{previewNoMessages}, renderTail(msgs, 80))
}

func TestRenderTailWhitespaceOnlyTextSkipped(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "   \n\t  "}},
		},
	}

	require.Equal(t, []string{previewNoMessages}, renderTail(msgs, 80))
}

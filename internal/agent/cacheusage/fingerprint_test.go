package cacheusage

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/stretchr/testify/require"
)

type stubTool struct {
	info fantasy.ToolInfo
	opts fantasy.ProviderOptions
}

func (s *stubTool) Info() fantasy.ToolInfo { return s.info }

func (s *stubTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{}, nil
}

func (s *stubTool) ProviderOptions() fantasy.ProviderOptions { return s.opts }

func (s *stubTool) SetProviderOptions(opts fantasy.ProviderOptions) { s.opts = opts }

func newStubTool(name string) fantasy.AgentTool {
	return &stubTool{info: fantasy.ToolInfo{
		Name:        name,
		Description: name + " does things",
		Parameters: map[string]any{
			"path":  map[string]any{"type": "string"},
			"limit": map[string]any{"type": "integer"},
		},
		Required: []string{"path"},
	}}
}

func cacheControl() fantasy.ProviderOptions {
	return fantasy.ProviderOptions{
		anthropic.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
	}
}

func reasoningMessage(signature string) fantasy.Message {
	return fantasy.Message{
		Role: fantasy.MessageRoleAssistant,
		Content: []fantasy.MessagePart{
			fantasy.ReasoningPart{
				Text: "thinking",
				ProviderOptions: fantasy.ProviderOptions{
					anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: signature},
				},
			},
			fantasy.TextPart{Text: "answer"},
		},
	}
}

func sampleMessages() []fantasy.Message {
	return []fantasy.Message{
		fantasy.NewSystemMessage("you are anvil"),
		fantasy.NewUserMessage("hello"),
		reasoningMessage("sig-1"),
		fantasy.NewUserMessage("next"),
	}
}

func TestComputeDeterministic(t *testing.T) {
	t.Parallel()

	tools := []fantasy.AgentTool{newStubTool("view"), newStubTool("edit")}
	a := Compute(tools, sampleMessages())
	b := Compute(tools, sampleMessages())

	require.Empty(t, a.Err)
	require.Equal(t, a, b)
	require.Len(t, a.ToolsHash, hashHexLen)
	require.Len(t, a.SystemHash, hashHexLen)
	require.Len(t, a.HistoryHash, hashHexLen)
	require.Equal(t, 2, a.ToolCount)
	require.Equal(t, 1, a.SystemCount)
	require.Equal(t, 3, a.MessageCount)
}

func TestComputeIgnoresMessageLevelCacheControl(t *testing.T) {
	t.Parallel()

	first := sampleMessages()
	first[0].ProviderOptions = cacheControl()
	first[2].ProviderOptions = cacheControl()
	first[3].ProviderOptions = cacheControl()

	moved := sampleMessages()
	moved[1].ProviderOptions = cacheControl()

	a := Compute(nil, first)
	b := Compute(nil, moved)
	require.Equal(t, a.SystemHash, b.SystemHash)
	require.Equal(t, a.HistoryHash, b.HistoryHash)
}

func TestComputePartLevelOptionChangesHistory(t *testing.T) {
	t.Parallel()

	a := sampleMessages()
	b := sampleMessages()
	b[2] = reasoningMessage("sig-2")

	fa, fb := Compute(nil, a), Compute(nil, b)
	require.Empty(t, fa.Err)
	require.Empty(t, fb.Err)
	require.NotEqual(t, fa.HistoryHash, fb.HistoryHash)
	require.Equal(t, fa.SystemHash, fb.SystemHash)
}

func TestComputeToolOrderChangesToolsHash(t *testing.T) {
	t.Parallel()

	a := Compute([]fantasy.AgentTool{newStubTool("view"), newStubTool("edit")}, nil)
	b := Compute([]fantasy.AgentTool{newStubTool("edit"), newStubTool("view")}, nil)
	require.NotEqual(t, a.ToolsHash, b.ToolsHash)
}

func TestComputeSystemBlockBoundaries(t *testing.T) {
	t.Parallel()

	two := Compute(nil, []fantasy.Message{
		fantasy.NewSystemMessage("ab"),
		fantasy.NewSystemMessage("cd"),
	})
	one := Compute(nil, []fantasy.Message{
		fantasy.NewSystemMessage("abcd"),
	})
	require.NotEqual(t, two.SystemHash, one.SystemHash)
	require.Equal(t, 2, two.SystemCount)
	require.Equal(t, 1, one.SystemCount)
}

func TestComputeMarshalErrorSetsErr(t *testing.T) {
	t.Parallel()

	bad := &stubTool{info: fantasy.ToolInfo{
		Name:       "bad",
		Parameters: map[string]any{"x": make(chan int)},
	}}
	f := Compute([]fantasy.AgentTool{bad}, sampleMessages())
	require.Empty(t, f.ToolsHash)
	require.Contains(t, f.Err, "tools")
	require.NotEmpty(t, f.HistoryHash)
	require.NotEmpty(t, f.SystemHash)
}

func TestPrefixMatchesAfterAppend(t *testing.T) {
	t.Parallel()

	prev := Compute(nil, sampleMessages())
	next := append(sampleMessages(),
		fantasy.NewUserMessage("more"),
		reasoningMessage("sig-3"),
	)
	cur := Compute(nil, next)

	match, ok := cur.PrefixMatches(prev.MessageCount, prev.HistoryHash)
	require.True(t, ok)
	require.True(t, match)
	require.NotEqual(t, prev.HistoryHash, cur.HistoryHash)
}

func TestPrefixMatchesAfterEdit(t *testing.T) {
	t.Parallel()

	prev := Compute(nil, sampleMessages())
	edited := sampleMessages()
	edited[1] = fantasy.NewUserMessage("hello, edited")
	edited = append(edited, fantasy.NewUserMessage("more"))
	cur := Compute(nil, edited)

	match, ok := cur.PrefixMatches(prev.MessageCount, prev.HistoryHash)
	require.True(t, ok)
	require.False(t, match)
}

func TestPrefixMatchesSystemChangeDoesNotAffectHistory(t *testing.T) {
	t.Parallel()

	prev := Compute(nil, sampleMessages())
	changed := sampleMessages()
	changed[0] = fantasy.NewSystemMessage("you are anvil, reloaded")
	cur := Compute(nil, changed)

	match, ok := cur.PrefixMatches(prev.MessageCount, prev.HistoryHash)
	require.True(t, ok)
	require.True(t, match)
	require.NotEqual(t, prev.SystemHash, cur.SystemHash)
}

func TestPrefixMatchesEdgeCases(t *testing.T) {
	t.Parallel()

	f := Compute(nil, sampleMessages())

	tests := []struct {
		name      string
		prevCount int
		prevHash  string
	}{
		{name: "zero count", prevCount: 0, prevHash: f.HistoryHash},
		{name: "negative count", prevCount: -1, prevHash: f.HistoryHash},
		{name: "beyond count", prevCount: f.MessageCount + 1, prevHash: f.HistoryHash},
		{name: "empty previous hash", prevCount: 1, prevHash: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			match, ok := f.PrefixMatches(tt.prevCount, tt.prevHash)
			require.False(t, ok)
			require.False(t, match)
		})
	}

	match, ok := f.PrefixMatches(f.MessageCount, f.HistoryHash)
	require.True(t, ok)
	require.True(t, match)
}

func TestComputeMediaSampling(t *testing.T) {
	t.Parallel()

	image := func(mutate func([]byte)) []fantasy.Message {
		data := make([]byte, 64<<10)
		for i := range data {
			data[i] = byte(i)
		}
		mutate(data)
		msg := fantasy.NewUserMessage("look", fantasy.FilePart{Filename: "a.png", Data: data, MediaType: "image/png"})
		media := fantasy.Message{
			Role: fantasy.MessageRoleTool,
			Content: []fantasy.MessagePart{fantasy.ToolResultPart{
				ToolCallID: "call-1",
				Output:     fantasy.ToolResultOutputContentMedia{Data: string(data), MediaType: "image/png"},
			}},
		}
		return []fantasy.Message{msg, media}
	}

	base := Compute(nil, image(func([]byte) {}))
	head := Compute(nil, image(func(d []byte) { d[0]++ }))
	tail := Compute(nil, image(func(d []byte) { d[len(d)-1]++ }))
	middle := Compute(nil, image(func(d []byte) { d[len(d)/2]++ }))

	require.Empty(t, base.Err)
	require.NotEqual(t, base.HistoryHash, head.HistoryHash)
	require.NotEqual(t, base.HistoryHash, tail.HistoryHash)
	require.Equal(t, base.HistoryHash, middle.HistoryHash, "middle bytes are not sampled")

	msgs := image(func([]byte) {})
	original := msgs[0].Content[1].(fantasy.FilePart).Data
	Compute(nil, msgs)
	require.Len(t, msgs[0].Content[1].(fantasy.FilePart).Data, len(original))
}

func TestComputeEmpty(t *testing.T) {
	t.Parallel()

	f := Compute(nil, nil)
	require.Empty(t, f.Err)
	require.Empty(t, f.HistoryHash)
	require.Zero(t, f.MessageCount)
	require.Len(t, f.ToolsHash, hashHexLen)
	require.Len(t, f.SystemHash, hashHexLen)

	_, ok := f.PrefixMatches(1, "abc")
	require.False(t, ok)
}

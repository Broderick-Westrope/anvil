package cacheusage

import (
	"context"
	"errors"
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

func TestComputeIgnoresPartLevelOptions(t *testing.T) {
	t.Parallel()

	a := sampleMessages()
	b := sampleMessages()
	b[2] = reasoningMessage("sig-2")
	c := sampleMessages()
	c[2].Content[0] = fantasy.ReasoningPart{Text: "thinking"}

	fa, fb, fc := Compute(nil, a), Compute(nil, b), Compute(nil, c)
	require.Empty(t, fa.Err)
	require.Equal(t, fa.HistoryHash, fb.HistoryHash)
	require.Equal(t, fa.HistoryHash, fc.HistoryHash)
}

func toolCall(id string) fantasy.ToolCallPart {
	return fantasy.ToolCallPart{ToolCallID: id, ToolName: "view", Input: `{"path":"a"}`}
}

func toolResult(id, text string) fantasy.ToolResultPart {
	return fantasy.ToolResultPart{ToolCallID: id, Output: fantasy.ToolResultOutputContentText{Text: text}}
}

// TestComputeMatchesDatabaseRebuild compares a step as fantasy builds it in
// memory with the same step as Anvil rebuilds it from the database.
func TestComputeMatchesDatabaseRebuild(t *testing.T) {
	t.Parallel()

	inMemory := []fantasy.Message{
		fantasy.NewUserMessage("hello"),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
			fantasy.ReasoningPart{Text: "\nfirst", ProviderOptions: cacheControl()},
			fantasy.TextPart{Text: "\nLooking"},
			fantasy.ReasoningPart{Text: ""},
			fantasy.ReasoningPart{Text: "\nsecond"},
			fantasy.TextPart{Text: " now\n"},
			fantasy.TextPart{Text: ""},
			toolCall("call-1"),
			toolCall("call-2"),
		}},
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
			toolResult("call-1", "one"),
			toolResult("call-2", "two"),
		}, ProviderOptions: cacheControl()},
	}
	rebuilt := []fantasy.Message{
		fantasy.NewUserMessage("hello"),
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
			fantasy.ReasoningPart{Text: "\nfirst\nsecond", ProviderOptions: fantasy.ProviderOptions{}},
			fantasy.TextPart{Text: "Looking now"},
			toolCall("call-1"),
			toolCall("call-2"),
		}},
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{toolResult("call-1", "one")}},
		{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{toolResult("call-2", "two")}},
		// Cancelled before it returned anything; Anvil skips these.
		{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{fantasy.TextPart{Text: " "}}},
	}

	a, b := Compute(nil, inMemory), Compute(nil, rebuilt)
	require.Empty(t, a.Err)
	require.Equal(t, a.HistoryHash, b.HistoryHash)
	require.Equal(t, a.PrefixLen(), b.PrefixLen())
	require.Equal(t, 7, a.PrefixLen())
	require.Equal(t, 3, a.MessageCount)
	require.Equal(t, 5, b.MessageCount)

	match, ok := Compute(nil, append(rebuilt, fantasy.NewUserMessage("next"))).PrefixMatches(a.PrefixLen(), a.HistoryHash)
	require.True(t, ok)
	require.True(t, match)
}

func TestComputeSemanticChangesHistory(t *testing.T) {
	t.Parallel()

	base := func() []fantasy.Message {
		return []fantasy.Message{
			fantasy.NewUserMessage("hello", fantasy.FilePart{Filename: "a.png", Data: []byte("png"), MediaType: "image/png"}),
			{Role: fantasy.MessageRoleAssistant, Content: []fantasy.MessagePart{
				fantasy.ReasoningPart{Text: "think"},
				fantasy.TextPart{Text: "answer"},
				toolCall("call-1"),
			}},
			{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
				toolResult("call-1", "result"),
				fantasy.ToolResultPart{ToolCallID: "call-2", Output: fantasy.ToolResultOutputContentError{Error: errors.New("boom")}},
				fantasy.ToolResultPart{ToolCallID: "call-3", Output: fantasy.ToolResultOutputContentMedia{Data: "aW1n", MediaType: "image/png", Text: "caption"}},
			}},
		}
	}
	edits := map[string]func(m []fantasy.Message){
		"user text": func(m []fantasy.Message) { m[0].Content[0] = fantasy.TextPart{Text: "hello!"} },
		"file data": func(m []fantasy.Message) {
			m[0].Content[1] = fantasy.FilePart{Data: []byte("gif"), MediaType: "image/png"}
		},
		"file type": func(m []fantasy.Message) {
			m[0].Content[1] = fantasy.FilePart{Data: []byte("png"), MediaType: "image/gif"}
		},
		"role":           func(m []fantasy.Message) { m[0].Role = fantasy.MessageRoleAssistant },
		"reasoning":      func(m []fantasy.Message) { m[1].Content[0] = fantasy.ReasoningPart{Text: "thought"} },
		"reasoning text": func(m []fantasy.Message) { m[1].Content[0] = fantasy.TextPart{Text: "think"} },
		"text":           func(m []fantasy.Message) { m[1].Content[1] = fantasy.TextPart{Text: "other"} },
		"call id":        func(m []fantasy.Message) { m[1].Content[2] = toolCall("call-9") },
		"call name": func(m []fantasy.Message) {
			m[1].Content[2] = fantasy.ToolCallPart{ToolCallID: "call-1", ToolName: "edit", Input: `{"path":"a"}`}
		},
		"call input": func(m []fantasy.Message) {
			m[1].Content[2] = fantasy.ToolCallPart{ToolCallID: "call-1", ToolName: "view", Input: `{"path":"b"}`}
		},
		"call provider executed": func(m []fantasy.Message) {
			m[1].Content[2] = fantasy.ToolCallPart{ToolCallID: "call-1", ToolName: "view", Input: `{"path":"a"}`, ProviderExecuted: true}
		},
		"result id":     func(m []fantasy.Message) { m[2].Content[0] = toolResult("call-9", "result") },
		"result output": func(m []fantasy.Message) { m[2].Content[0] = toolResult("call-1", "other") },
		"result provider executed": func(m []fantasy.Message) {
			m[2].Content[0] = fantasy.ToolResultPart{ToolCallID: "call-1", Output: fantasy.ToolResultOutputContentText{Text: "result"}, ProviderExecuted: true}
		},
		"result error": func(m []fantasy.Message) {
			m[2].Content[1] = fantasy.ToolResultPart{ToolCallID: "call-2", Output: fantasy.ToolResultOutputContentError{Error: errors.New("bang")}}
		},
		"result error to text": func(m []fantasy.Message) { m[2].Content[1] = toolResult("call-2", "boom") },
		"result nil error": func(m []fantasy.Message) {
			m[2].Content[1] = fantasy.ToolResultPart{ToolCallID: "call-2", Output: fantasy.ToolResultOutputContentError{}}
		},
		"result nil output": func(m []fantasy.Message) { m[2].Content[1] = fantasy.ToolResultPart{ToolCallID: "call-2"} },
		"result media": func(m []fantasy.Message) {
			m[2].Content[2] = fantasy.ToolResultPart{ToolCallID: "call-3", Output: fantasy.ToolResultOutputContentMedia{Data: "Z2lm", MediaType: "image/png", Text: "caption"}}
		},
		"result media type": func(m []fantasy.Message) {
			m[2].Content[2] = fantasy.ToolResultPart{ToolCallID: "call-3", Output: fantasy.ToolResultOutputContentMedia{Data: "aW1n", MediaType: "image/gif", Text: "caption"}}
		},
		"result media text": func(m []fantasy.Message) {
			m[2].Content[2] = fantasy.ToolResultPart{ToolCallID: "call-3", Output: fantasy.ToolResultOutputContentMedia{Data: "aW1n", MediaType: "image/png"}}
		},
		"part order": func(m []fantasy.Message) { m[2].Content[0], m[2].Content[1] = m[2].Content[1], m[2].Content[0] },
	}

	want := Compute(nil, base())
	require.Empty(t, want.Err)
	require.Equal(t, 8, want.PrefixLen())
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msgs := base()
			edit(msgs)
			got := Compute(nil, msgs)
			require.Empty(t, got.Err)
			require.NotEqual(t, want.HistoryHash, got.HistoryHash)
		})
	}
}

func TestComputeIgnoresFileName(t *testing.T) {
	t.Parallel()

	a := Compute(nil, []fantasy.Message{fantasy.NewUserMessage("x", fantasy.FilePart{Filename: "a.png", Data: []byte("png"), MediaType: "image/png"})})
	b := Compute(nil, []fantasy.Message{fantasy.NewUserMessage("x", fantasy.FilePart{Filename: "/tmp/a.png", Data: []byte("png"), MediaType: "image/png"})})
	require.Equal(t, a.HistoryHash, b.HistoryHash)
}

// otherOutput is a tool result output type the fingerprint does not know.
type otherOutput struct{ Value string }

func (otherOutput) GetType() fantasy.ToolResultContentType { return "other" }

// otherPart is a message part type the fingerprint does not know.
type otherPart struct{ Value any }

func (otherPart) GetType() fantasy.ContentType { return "other" }

func (otherPart) Options() fantasy.ProviderOptions { return nil }

func TestComputeUnknownTypes(t *testing.T) {
	t.Parallel()

	withOutput := func(v string) []fantasy.Message {
		return []fantasy.Message{{Role: fantasy.MessageRoleTool, Content: []fantasy.MessagePart{
			fantasy.ToolResultPart{ToolCallID: "call-1", Output: otherOutput{Value: v}},
		}}}
	}
	a, b := Compute(nil, withOutput("a")), Compute(nil, withOutput("b"))
	require.Empty(t, a.Err)
	require.NotEqual(t, a.HistoryHash, b.HistoryHash)

	withPart := func(v any) []fantasy.Message {
		return []fantasy.Message{
			fantasy.NewUserMessage("first"),
			{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{otherPart{Value: v}}},
			fantasy.NewUserMessage("last"),
		}
	}
	pa, pb := Compute(nil, withPart("a")), Compute(nil, withPart("b"))
	require.Empty(t, pa.Err)
	require.NotEqual(t, pa.HistoryHash, pb.HistoryHash)
	require.Equal(t, 3, pa.PrefixLen())

	bad := Compute(nil, withPart(make(chan int)))
	require.Contains(t, bad.Err, "message 1")
	require.Empty(t, bad.HistoryHash)
	require.Equal(t, 3, bad.MessageCount)
	require.Equal(t, 1, bad.PrefixLen())
	_, ok := bad.PrefixMatches(1, pa.HistoryHash)
	require.True(t, ok)
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

	match, ok := cur.PrefixMatches(prev.PrefixLen(), prev.HistoryHash)
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

	match, ok := cur.PrefixMatches(prev.PrefixLen(), prev.HistoryHash)
	require.True(t, ok)
	require.False(t, match)
}

func TestPrefixMatchesSystemChangeDoesNotAffectHistory(t *testing.T) {
	t.Parallel()

	prev := Compute(nil, sampleMessages())
	changed := sampleMessages()
	changed[0] = fantasy.NewSystemMessage("you are anvil, reloaded")
	cur := Compute(nil, changed)

	match, ok := cur.PrefixMatches(prev.PrefixLen(), prev.HistoryHash)
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
		{name: "beyond count", prevCount: f.PrefixLen() + 1, prevHash: f.HistoryHash},
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

	match, ok := f.PrefixMatches(f.PrefixLen(), f.HistoryHash)
	require.True(t, ok)
	require.True(t, match)

	match, ok = f.PrefixMatches(1, f.HistoryHash)
	require.True(t, ok)
	require.False(t, match)
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

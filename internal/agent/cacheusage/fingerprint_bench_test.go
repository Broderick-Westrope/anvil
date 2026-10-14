package cacheusage

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"charm.land/fantasy"
)

func benchmarkMessages(n int) []fantasy.Message {
	rng := rand.New(rand.NewPCG(1, 2))
	image := make([]byte, 1<<20)
	for i := range image {
		image[i] = byte(rng.Uint32())
	}

	mediaAt := n / 2 / 4 * 4
	messages := []fantasy.Message{
		fantasy.NewSystemMessage(strings.Repeat("system prompt ", 2000)),
		fantasy.NewSystemMessage("project context"),
	}
	for i := range n {
		switch i % 4 {
		case 0:
			msg := fantasy.NewUserMessage(fmt.Sprintf("user turn %d %s", i, strings.Repeat("x", 400)))
			if i == mediaAt {
				msg.Content = append(msg.Content, fantasy.FilePart{
					Filename:  "screenshot.png",
					Data:      image,
					MediaType: "image/png",
				})
			}
			messages = append(messages, msg)
		case 1:
			messages = append(messages, reasoningMessage(fmt.Sprintf("sig-%d", i)))
		case 2:
			messages = append(messages, fantasy.Message{
				Role: fantasy.MessageRoleAssistant,
				Content: []fantasy.MessagePart{fantasy.ToolCallPart{
					ToolCallID: fmt.Sprintf("call-%d", i),
					ToolName:   "view",
					Input:      `{"file_path":"/tmp/x.go"}`,
				}},
			})
		case 3:
			var output fantasy.ToolResultOutputContent = fantasy.ToolResultOutputContentText{
				Text: strings.Repeat("line of file content\n", 100),
			}
			if i == mediaAt+3 {
				output = fantasy.ToolResultOutputContentMedia{
					Data:      base64.StdEncoding.EncodeToString(image[:256<<10]),
					MediaType: "image/png",
				}
			}
			messages = append(messages, fantasy.Message{
				Role: fantasy.MessageRoleTool,
				Content: []fantasy.MessagePart{fantasy.ToolResultPart{
					ToolCallID: fmt.Sprintf("call-%d", i-1),
					Output:     output,
				}},
			})
		}
	}
	return messages
}

func BenchmarkCompute(b *testing.B) {
	tools := make([]fantasy.AgentTool, 0, 40)
	for i := range 40 {
		tools = append(tools, newStubTool(fmt.Sprintf("tool_%d", i)))
	}
	messages := benchmarkMessages(500)

	b.ReportAllocs()
	for b.Loop() {
		f := Compute(tools, messages)
		if f.Err != "" {
			b.Fatal(f.Err)
		}
	}
}

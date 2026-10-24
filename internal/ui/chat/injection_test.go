package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestInjectedMessagesRenderWithTheirSource(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inj      message.InjectionContent
		want     string
		withText bool
	}{
		"human steer": {
			inj:  message.InjectionContent{Source: message.InjectionSourceHuman, Kind: message.InjectionKindSteer, Text: "use the v2 API"},
			want: "You · steer", withText: true,
		},
		"human interrupt": {
			inj:  message.InjectionContent{Source: message.InjectionSourceHuman, Kind: message.InjectionKindInterruptSteer, Text: "stop"},
			want: "You · interrupt", withText: true,
		},
		"orchestrator resume": {
			inj:  message.InjectionContent{Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, Text: "now add tests"},
			want: "Orchestrator · resume", withText: true,
		},
		"unknown harness kind is never labelled as you": {
			inj:  message.InjectionContent{Source: message.InjectionSourceHarness, Kind: "future", Text: "x"},
			want: "Harness",
		},
		"harness retry": {
			inj: message.InjectionContent{
				Source: message.InjectionSourceHarness, Kind: message.InjectionKindRetry, Text: "Continue.",
				Attempt: 1, MaxAttempts: 2, Reason: "503 overloaded",
			},
			want: "Harness · retry 1/2 after 503 overloaded",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sty := styles.TokyoNight()
			msg := &message.Message{
				ID: "m", Role: message.User, MessageType: message.MessageTypeSubagentInjection,
				Parts: message.InjectionParts([]message.InjectionContent{tc.inj}),
			}
			items := ExtractMessageItems(&sty, msg, nil, ToolItemOptions{}, nil)
			require.Len(t, items, 1)
			out := ansi.Strip(items[0].Render(100))
			require.Contains(t, out, tc.want)
			require.NotContains(t, out, "injected_message")
			if tc.withText {
				require.Contains(t, out, tc.inj.Text)
			}
		})
	}
}

package agent

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestTrimFailedAttemptMessagesOnlyRemovesAcceptedPrompt(t *testing.T) {
	t.Parallel()
	accepted := message.Message{ID: "accepted", Role: message.User}
	other := message.Message{ID: "other", Role: message.User}
	synthetic := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "summary"}}}
	for _, test := range []struct {
		name        string
		input, want []message.Message
	}{
		{"root", []message.Message{accepted}, []message.Message{}},
		{"selected history", []message.Message{other, synthetic, accepted}, []message.Message{other, synthetic}},
		{"compacted away", []message.Message{synthetic}, []message.Message{synthetic}},
		{"empty", nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := trimFailedAttemptMessages(test.input, "accepted")
			require.Equal(t, test.want, got)
		})
	}
}

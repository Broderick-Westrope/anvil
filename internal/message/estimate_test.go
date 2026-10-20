package message_test

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestEstimateMessageTokens_CountsBranchSummaryText(t *testing.T) {
	t.Parallel()

	part := message.BranchSummaryContent{Summary: strings.Repeat("a", 400)}
	got := message.EstimateMessageTokens(message.Message{Parts: []message.ContentPart{part}})
	require.Equal(t, 100, got)
}

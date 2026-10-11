package agent

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

// TestSummarizeFailure covers the failed summaries the provider fixture in
// run_integration_test.go can't script: a failure after reasoning and text
// have streamed, and a canceled request that fails with an unrelated
// error.
func TestSummarizeFailure(t *testing.T) {
	t.Parallel()
	streamed := []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeReasoningStart, ID: "r"},
		{Type: fantasy.StreamPartTypeReasoningDelta, ID: "r", Delta: "Thinking."},
		{Type: fantasy.StreamPartTypeReasoningEnd, ID: "r", ProviderMetadata: fantasy.ProviderMetadata{
			anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: "sig"},
		}},
		{Type: fantasy.StreamPartTypeTextStart, ID: "t"},
		{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: "Partial summary."},
		{Type: fantasy.StreamPartTypeTextEnd, ID: "t"},
	}
	badRequest := &fantasy.ProviderError{Title: "bad request", Message: "nope", StatusCode: http.StatusBadRequest}
	tests := map[string]struct {
		// fail returns the error that ends the stream after the streamed
		// parts. cancel cancels the summarize context.
		fail     func(cancel context.CancelFunc) error
		wantErr  error
		abandons bool
	}{
		"provider error keeps what streamed": {
			fail:    func(context.CancelFunc) error { return badRequest },
			wantErr: badRequest,
		},
		"canceled context drops the placeholder": {
			fail: func(cancel context.CancelFunc) error {
				cancel()
				return errors.New("stream broke")
			},
			abandons: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				return append(slices.Clone(streamed), fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: tt.fail(cancel)}), nil
			}}
			m := Model{Model: model, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
			a := sideCallAgent(env, m, m, "", config.ProviderConfig{})
			sessionID, leafID := sessionWithUserMessage(t, env)

			err := a.summarizeOwned(ctx, sessionID, nil)
			require.Error(t, err)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}

			sess, getErr := env.sessions.Get(t.Context(), sessionID)
			require.NoError(t, getErr)
			msgs, listErr := env.messages.List(t.Context(), sessionID)
			require.NoError(t, listErr)
			idx := slices.IndexFunc(msgs, func(m message.Message) bool { return m.MessageType == message.MessageTypeCompaction })
			if tt.abandons {
				require.Equal(t, -1, idx)
				require.Equal(t, leafID, sess.LeafMessageID)
				return
			}
			require.GreaterOrEqual(t, idx, 0)
			placeholder := msgs[idx]
			require.Equal(t, placeholder.ID, sess.LeafMessageID)
			require.Equal(t, "Thinking.", placeholder.ReasoningContent().Thinking)
			require.Equal(t, "sig", placeholder.ReasoningContent().Signature)
			require.Equal(t, "Partial summary.", placeholder.Content().Text)
			finish := placeholder.FinishPart()
			require.NotNil(t, finish)
			require.Equal(t, message.FinishReasonError, finish.Reason)
			require.Equal(t, "Summarization Error", finish.Message)
			require.Equal(t, tt.wantErr.Error(), finish.Details)
		})
	}
}

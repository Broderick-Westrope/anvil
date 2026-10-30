package agent

import (
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/stretchr/testify/require"
)

// Without fine-grained tool streaming Anthropic sends nothing while the
// model writes a large tool parameter, so a long write trips the idle
// timeout. Only Anthropic's own API gets the beta.
func TestFineGrainedToolStreamingBeta(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	for name, tc := range map[string]struct {
		provider config.ProviderConfig
		want     bool
	}{
		"anthropic api key": {
			provider: config.ProviderConfig{ID: "anthropic", Type: catwalk.TypeAnthropic, APIKey: "sk-ant"},
			want:     true,
		},
		"anthropic oauth": {
			provider: config.ProviderConfig{ID: "anthropic", Type: catwalk.TypeAnthropic, APIKey: "Bearer at", OAuthToken: &oauth.Token{AccessToken: "at", ExpiresAt: 9999999999}},
			want:     true,
		},
		"anthropic with extra betas": {
			provider: config.ProviderConfig{ID: "anthropic", Type: catwalk.TypeAnthropic, APIKey: "sk-ant", ExtraHeaders: map[string]string{"anthropic-beta": "custom-beta"}},
			want:     true,
		},
		"anthropic-compatible endpoint": {
			provider: config.ProviderConfig{ID: "minimax", Type: catwalk.TypeAnthropic, APIKey: "key"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, got := openAIStub(t)
			tc.provider.BaseURL = srv.URL
			streamOnce(t, tc.provider, "claude-sonnet-4-5")

			req := got()
			require.Equal(t, "/v1/messages", req.path)
			betas := strings.Split(req.headers.Get("Anthropic-Beta"), ",")
			if tc.want {
				require.Contains(t, betas, fineGrainedToolStreamingBeta)
			} else {
				require.NotContains(t, betas, fineGrainedToolStreamingBeta)
			}
			if extra := tc.provider.ExtraHeaders["anthropic-beta"]; extra != "" {
				require.Contains(t, betas, extra, "configured betas are kept")
			}
		})
	}
}

package dialog

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func openAIModelIDs(m *Models) []string {
	var ids []string
	for i := range m.list.Len() {
		if mi, ok := m.list.ItemAt(i).(*ModelItem); ok && mi.prov.ID == catwalk.InferenceProviderOpenAI {
			ids = append(ids, mi.model.ID)
		}
	}
	return ids
}

func TestModels_SignedInProviderListsOnlyThePlanCatalog(t *testing.T) {
	sty := styles.TokyoNight()
	newModels := func(pc config.ProviderConfig) *Models {
		cfg := testCommandsConfig()
		cfg.Options = &config.Options{DisableProviderAutoUpdate: true}
		cfg.Providers.Set("openai", pc)
		m, err := NewModels(&common.Common{Styles: &sty, Workspace: &bouncerCommandsWorkspace{cfg: cfg}}, false)
		require.NoError(t, err)
		return m
	}

	apiOnly := newModels(config.ProviderConfig{ID: "openai", APIKey: "sk"})
	apiIDs := openAIModelIDs(apiOnly)
	require.NotEmpty(t, apiIDs, "the API catalog comes from the embedded providers")

	signed := newModels(config.ProviderConfig{
		ID:            "openai",
		OAuthToken:    &oauth.Token{AccessToken: "at"},
		ChatGPTModels: []catwalk.Model{{ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex"}, {ID: "gpt-5.1-codex-mini", Name: "Mini"}},
	})
	require.ElementsMatch(t, []string{"gpt-5.1-codex", "gpt-5.1-codex-mini"}, openAIModelIDs(signed))

	pending := newModels(config.ProviderConfig{ID: "openai", OAuthToken: &oauth.Token{AccessToken: "at"}})
	require.ElementsMatch(t, apiIDs, openAIModelIDs(pending), "until the plan's catalog arrives the API catalog shows")
}

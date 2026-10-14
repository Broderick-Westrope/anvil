package model

import (
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/env"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

type authWorkspace struct {
	*testWorkspace
}

// Resolver resolves templates against an empty environment, so a bare
// $OPENAI_API_KEY is not a credential.
func (authWorkspace) Resolver() config.VariableResolver {
	return config.NewShellVariableResolver(env.NewFromMap(map[string]string{}))
}

var (
	openAIProvider = catwalk.Provider{ID: catwalk.InferenceProviderOpenAI, Name: "OpenAI"}
	xAIProvider    = catwalk.Provider{ID: catwalk.InferenceProviderXAI, Name: "xAI"}
)

// newAuthUI returns a UI whose config holds the given providers.
func newAuthUI(t *testing.T, providers ...config.ProviderConfig) *UI {
	t.Helper()
	m := csync.NewMap[string, config.ProviderConfig]()
	for _, p := range providers {
		m.Set(p.ID, p)
	}
	cfg := &config.Config{Providers: m, Options: &config.Options{DisableProviderAutoUpdate: true}}
	u := newTestUI()
	u.com.Workspace = authWorkspace{&testWorkspace{cfg: cfg}}
	u.dialog = dialog.NewOverlay()
	return u
}

func signedIn(id string, catalog ...string) config.ProviderConfig {
	pc := config.ProviderConfig{ID: id, OAuthToken: &oauth.Token{AccessToken: "at"}}
	for _, m := range catalog {
		if id == "xai" {
			pc.GrokModels = append(pc.GrokModels, catwalk.Model{ID: m})
		} else {
			pc.ChatGPTModels = append(pc.ChatGPTModels, catwalk.Model{ID: m})
		}
	}
	return pc
}

func selectModel(p catwalk.Provider, model string) dialog.ActionSelectModel {
	return dialog.ActionSelectModel{
		Provider:  p,
		Model:     config.SelectedModel{Provider: string(p.ID), Model: model},
		ModelType: config.SelectedModelTypeLarge,
	}
}

// frontDialogText advances an OAuth dialog past its initialising spinner,
// as the started sign-in would, and returns the drawn screen.
func frontDialogText(t *testing.T, u *UI) string {
	t.Helper()
	u.dialog.Update(dialog.ActionInitiateOAuth{VerificationURL: "https://example.test/authorize"})
	scr := uv.NewScreenBuffer(120, 40)
	u.dialog.Draw(scr, scr.Bounds())
	var b strings.Builder
	for y := range 40 {
		for x := range 120 {
			if c := scr.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestSubscriptionSelect_NoCredentialAsksHowToAuthenticate(t *testing.T) {
	t.Parallel()

	for _, p := range []catwalk.Provider{openAIProvider, xAIProvider} {
		u := newAuthUI(t, config.ProviderConfig{ID: string(p.ID), APIKey: "$UNSET_KEY"})

		_, handled := u.handleSubscriptionProviderSelect(selectModel(p, "some-model"))

		require.True(t, handled, string(p.ID))
		require.True(t, u.dialog.ContainsDialog(dialog.AuthMethodID), "%s: the method chooser opens", p.ID)
	}
}

func TestSubscriptionSelect_APIKeyAndCatalogModelsPassThrough(t *testing.T) {
	t.Parallel()

	u := newAuthUI(t, config.ProviderConfig{ID: "openai", APIKey: "sk-real"})
	_, handled := u.handleSubscriptionProviderSelect(selectModel(openAIProvider, "gpt-5"))
	require.False(t, handled, "an API key is a credential; normal selection applies")

	u = newAuthUI(t, signedIn("openai", "gpt-5.1-codex"))
	_, handled = u.handleSubscriptionProviderSelect(selectModel(openAIProvider, "gpt-5.1-codex"))
	require.False(t, handled, "a model the plan offers is selected normally")
}

func TestSubscriptionSelect_ModelTheAccountLacksReopensTheList(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pc config.ProviderConfig
		p  catwalk.Provider
	}{
		{signedIn("openai", "gpt-5.1-codex"), openAIProvider},
		{signedIn("xai", "grok-4-fast"), xAIProvider},
	} {
		u := newAuthUI(t, tc.pc)
		u.dialog.OpenDialog(dialog.NewAuthMethod(u.com, false, tc.p, config.SelectedModel{}, config.SelectedModelTypeLarge))

		cmd, handled := u.handleSubscriptionProviderSelect(selectModel(tc.p, "api-only-model"))

		require.True(t, handled)
		require.NotNil(t, cmd, "warns and reopens the models list")
		require.False(t, u.dialog.ContainsDialog(dialog.AuthMethodID), "the sign-in dialogs are closed")
		require.True(t, u.dialog.ContainsDialog(dialog.ModelsID), "the list reopens with the plan's catalog")
	}
}

func TestOpenAuthenticationDialog_PicksTheDialogForTheCredential(t *testing.T) {
	t.Parallel()

	model := config.SelectedModel{Model: "m"}
	for name, tc := range map[string]struct {
		pc       config.ProviderConfig
		provider catwalk.Provider
		wantID   string
		wantText string
	}{
		"openai, no credential":       {config.ProviderConfig{ID: "openai"}, openAIProvider, dialog.AuthMethodID, ""},
		"openai, api key":             {config.ProviderConfig{ID: "openai", APIKey: "sk"}, openAIProvider, dialog.APIKeyInputID, ""},
		"openai, signed in":           {signedIn("openai"), openAIProvider, dialog.OAuthID, "ChatGPT"},
		"xai, signed in":              {signedIn("xai"), xAIProvider, dialog.OAuthID, "Grok"},
		"xai, no credential":          {config.ProviderConfig{ID: "xai"}, xAIProvider, dialog.AuthMethodID, ""},
		"other provider is unchanged": {config.ProviderConfig{ID: "groq"}, catwalk.Provider{ID: "groq", Name: "Groq"}, dialog.APIKeyInputID, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u := newAuthUI(t, tc.pc)

			u.openAuthenticationDialog(tc.provider, model, config.SelectedModelTypeLarge)

			require.True(t, u.dialog.ContainsDialog(tc.wantID))
			if tc.wantText != "" {
				require.Contains(t, frontDialogText(t, u), tc.wantText)
			}
		})
	}
}

func TestOpenAuthenticationDialogWithMethod(t *testing.T) {
	t.Parallel()

	model := config.SelectedModel{Model: "m"}

	u := newAuthUI(t, config.ProviderConfig{ID: "xai"})
	u.openAuthenticationDialogWithMethod(xAIProvider, model, config.SelectedModelTypeLarge, true)
	require.True(t, u.dialog.ContainsDialog(dialog.OAuthID))
	require.Contains(t, frontDialogText(t, u), "Grok")

	u = newAuthUI(t, config.ProviderConfig{ID: "openai"})
	u.openAuthenticationDialogWithMethod(openAIProvider, model, config.SelectedModelTypeLarge, false)
	require.True(t, u.dialog.ContainsDialog(dialog.APIKeyInputID))
}

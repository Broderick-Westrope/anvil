package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolWebSearch_Validate(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		search  ToolWebSearch
		wantErr string
	}{
		"unset":               {},
		"duckduckgo":          {search: ToolWebSearch{Provider: WebSearchDuckDuckGo}},
		"duckduckgo with key": {search: ToolWebSearch{Provider: WebSearchDuckDuckGo, APIKey: "unused"}},
		"brave":               {search: ToolWebSearch{Provider: WebSearchBrave, APIKey: "$BRAVE_API_KEY"}},
		"tavily":              {search: ToolWebSearch{Provider: WebSearchTavily, APIKey: "$TAVILY_API_KEY"}},
		"brave without key":   {search: ToolWebSearch{Provider: WebSearchBrave}, wantErr: `provider "brave" needs api_key`},
		"tavily without key":  {search: ToolWebSearch{Provider: WebSearchTavily}, wantErr: `provider "tavily" needs api_key`},
		"unknown":             {search: ToolWebSearch{Provider: "google", APIKey: "k"}, wantErr: `unknown provider "google" (want duckduckgo, brave or tavily)`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tc.search.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tc.wantErr)
		})
	}
}

func TestLoadFromBytes_WebToolsMerge(t *testing.T) {
	t.Parallel()
	global := []byte(`{"tools":{"web_search":{"provider":"brave","api_key":"$BRAVE_API_KEY"},"fetch":{"github_token":"$GH_PUBLIC"}}}`)
	project := []byte(`{"tools":{"web_search":{"api_key":"$(security find-generic-password -w -s brave)"}}}`)

	cfg, err := loadFromBytes([][]byte{global, project})

	require.NoError(t, err)
	require.Equal(t, ToolWebSearch{Provider: WebSearchBrave, APIKey: "$(security find-generic-password -w -s brave)"}, cfg.Tools.WebSearch)
	require.Equal(t, ToolFetch{GitHubToken: "$GH_PUBLIC"}, cfg.Tools.Fetch)
}

func webSearchLoadDirs(t *testing.T) (work, data string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	work = filepath.Join(dir, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	return work, filepath.Join(dir, "data")
}

func TestLoadRejectsAnUnknownWebSearchProvider(t *testing.T) {
	work, data := webSearchLoadDirs(t)
	require.NoError(t, os.WriteFile(filepath.Join(work, "anvil.json"),
		[]byte(`{"tools":{"web_search":{"provider":"bing","api_key":"k"}}}`), 0o644))

	_, err := Load(work, data, false)

	require.ErrorContains(t, err, `invalid tools.web_search configuration: unknown provider "bing"`)
}

func TestReloadRejectsAWebSearchAPIWithoutKeyAndKeepsTheRunningConfig(t *testing.T) {
	work, data := webSearchLoadDirs(t)
	path := filepath.Join(work, "anvil.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"tools":{"web_search":{"provider":"duckduckgo"}}}`), 0o644))
	store, err := Load(work, data, false)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte(`{"tools":{"web_search":{"provider":"tavily"}}}`), 0o644))
	err = store.ReloadFromDisk(context.Background())

	require.ErrorContains(t, err, `invalid tools.web_search configuration on reload: provider "tavily" needs api_key`)
	require.Equal(t, WebSearchDuckDuckGo, store.Config().Tools.WebSearch.Provider)
}

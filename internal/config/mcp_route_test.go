package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateMCPRoutes(t *testing.T) {
	t.Parallel()
	const lazyDirect = `mcp "srv": lazy_description and route "direct" can't be combined; ` +
		`remove route to reach it through the catalogue, or remove lazy_description ` +
		`to connect it at startup and list it directly`
	tests := map[string]struct {
		route   MCPRoute
		lazy    string
		wantErr string
	}{
		"unset":          {route: ""},
		"auto":           {route: MCPRouteAuto},
		"catalogue":      {route: MCPRouteCatalogue},
		"direct":         {route: MCPRouteDirect},
		"unknown":        {route: "lazy", wantErr: `mcp "srv": unknown route "lazy" (want auto, catalogue or direct)`},
		"lazy unset":     {route: "", lazy: "Lazy server."},
		"lazy auto":      {route: MCPRouteAuto, lazy: "Lazy server."},
		"lazy catalogue": {route: MCPRouteCatalogue, lazy: "Lazy server."},
		"lazy direct":    {route: MCPRouteDirect, lazy: "Lazy server.", wantErr: lazyDirect},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := &Config{MCP: MCPs{"srv": {Type: MCPStdio, Command: "x", Route: tc.route, LazyDescription: tc.lazy}}}
			err := c.ValidateMCPRoutes()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tc.wantErr)
		})
	}
}

func TestLoadRejectsAnUnknownMCPRoute(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	work := filepath.Join(dir, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "anvil.json"),
		[]byte(`{"mcp":{"srv":{"type":"stdio","command":"x","route":"sometimes"}}}`), 0o644))

	_, err := Load(work, filepath.Join(dir, "data"), false)
	require.ErrorContains(t, err, `unknown route "sometimes"`)
}

func TestLoadRejectsALazyDirectMCP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	work := filepath.Join(dir, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "anvil.json"),
		[]byte(`{"mcp":{"srv":{"type":"stdio","command":"x","route":"direct","lazy_description":"Lazy."}}}`), 0o644))

	_, err := Load(work, filepath.Join(dir, "data"), false)
	require.ErrorContains(t, err, `mcp "srv": lazy_description and route "direct" can't be combined`)
}

func TestReloadRejectsALazyDirectMCPAndKeepsTheRunningConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	work := filepath.Join(dir, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	path := filepath.Join(work, "anvil.json")
	require.NoError(t, os.WriteFile(path,
		[]byte(`{"mcp":{"srv":{"type":"stdio","command":"x","lazy_description":"Lazy."}}}`), 0o644))
	store, err := Load(work, filepath.Join(dir, "data"), false)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path,
		[]byte(`{"mcp":{"srv":{"type":"stdio","command":"x","route":"direct","lazy_description":"Lazy."}}}`), 0o644))
	err = store.ReloadFromDisk(context.Background())

	require.ErrorContains(t, err, `mcp "srv": lazy_description and route "direct" can't be combined`)
	require.Empty(t, store.Config().MCP["srv"].Route)
}

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateMCPRoutes(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		route   MCPRoute
		wantErr string
	}{
		"unset":     {route: ""},
		"auto":      {route: MCPRouteAuto},
		"catalogue": {route: MCPRouteCatalogue},
		"direct":    {route: MCPRouteDirect},
		"unknown":   {route: "lazy", wantErr: `mcp "srv": unknown route "lazy" (want auto, catalogue or direct)`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := &Config{MCP: MCPs{"srv": {Type: MCPStdio, Command: "x", Route: tc.route}}}
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

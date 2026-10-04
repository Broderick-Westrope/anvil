package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestartRequired(t *testing.T) {
	t.Parallel()

	ptr := func(v float64) *float64 { return &v }
	yes := true
	base := func() *Config {
		return &Config{
			MCP:     MCPs{"docs": {Type: MCPStdio, Command: "docs-mcp"}},
			LSP:     LSPs{"gopls": {Command: "gopls"}},
			Options: &Options{TUI: &TUIOptions{}},
		}
	}
	bouncer := func(mutate func(*Bouncer)) *TrustedBouncer {
		b := &Bouncer{
			Mode:  BouncerShadow,
			URL:   "https://bouncer.example/predict",
			Model: "von-1.0.0",
		}
		if mutate != nil {
			mutate(b)
		}
		return &TrustedBouncer{Config: b, APIKey: "key"}
	}

	tests := []struct {
		name       string
		mutate     func(*Config)
		startupB   *TrustedBouncer
		curB       *TrustedBouncer
		startupRaw string
		curRaw     string
		want       []string
	}{
		{
			name: "no changes",
		},
		{
			name:     "same bouncer",
			startupB: bouncer(nil),
			curB:     bouncer(nil),
		},
		{
			name:     "thresholds only",
			startupB: bouncer(nil),
			curB: bouncer(func(b *Bouncer) {
				b.EscalateAt = ptr(0.4)
				b.EscalateAtAxes = map[string]float64{"destructive": 0.3}
				b.ConcernAt = ptr(0.2)
				b.SeverityConcern = ptr(1)
				b.DenyAt = ptr(0.95)
				b.SeverityEscalate = ptr(2.5)
				b.UserRequestedAt = ptr(0.8)
			}),
		},
		{
			name: "mcp server added",
			mutate: func(c *Config) {
				c.MCP["extra"] = MCPConfig{Type: MCPHttp, URL: "https://mcp.example"}
			},
			want: []string{"mcp"},
		},
		{
			name: "lsp args changed",
			mutate: func(c *Config) {
				c.LSP["gopls"] = LSPConfig{Command: "gopls", Args: []string{"-remote=auto"}}
			},
			want: []string{"lsp"},
		},
		{
			name: "all servers removed",
			mutate: func(c *Config) {
				c.MCP = nil
				c.LSP = nil
			},
			want: []string{"mcp", "lsp"},
		},
		{
			name:     "bouncer added",
			curB:     bouncer(nil),
			startupB: nil,
			want:     []string{"bouncer"},
		},
		{
			name:     "bouncer removed",
			startupB: bouncer(nil),
			want:     []string{"bouncer"},
		},
		{
			name:     "bouncer with nil config treated as absent",
			startupB: &TrustedBouncer{},
		},
		{
			name:     "bouncer connection changed",
			startupB: bouncer(nil),
			curB: bouncer(func(b *Bouncer) {
				b.URL = "https://other.example/predict"
				b.SendUserMessages = &yes
			}),
			want: []string{"bouncer"},
		},
		{
			name:     "bouncer mode changed",
			startupB: bouncer(nil),
			curB:     bouncer(func(b *Bouncer) { b.Mode = BouncerEnforce }),
			want:     []string{"bouncer.mode"},
		},
		{
			name:     "bouncer mode unset equals off",
			startupB: bouncer(func(b *Bouncer) { b.Mode = "" }),
			curB:     bouncer(func(b *Bouncer) { b.Mode = BouncerOff }),
		},
		{
			name:       "project directory changed",
			startupRaw: ".anvil",
			curRaw:     "state",
			want:       []string{"options.project_directory"},
		},
		{
			name:   "compact mode changed",
			mutate: func(c *Config) { c.Options.TUI.CompactMode = true },
			want:   []string{"options.tui"},
		},
		{
			name:   "transparent unset equals false",
			mutate: func(c *Config) { no := false; c.Options.TUI.Transparent = &no },
		},
		{
			name: "live tui options are ignored",
			mutate: func(c *Config) {
				c.Options.TUI.DiffMode = "split"
				depth := 3
				c.Options.TUI.Completions.MaxDepth = &depth
			},
		},
		{
			name: "everything changed",
			mutate: func(c *Config) {
				c.MCP = MCPs{}
				c.LSP = LSPs{}
				c.Options.TUI.Transparent = &yes
			},
			startupB:   bouncer(nil),
			curB:       bouncer(func(b *Bouncer) { b.Model = "von-2"; b.Mode = BouncerOff }),
			startupRaw: "a",
			curRaw:     "b",
			want:       []string{"mcp", "lsp", "bouncer", "bouncer.mode", "options.project_directory", "options.tui"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cur := base()
			if tc.mutate != nil {
				tc.mutate(cur)
			}
			got := RestartRequired(base(), cur, tc.startupB, tc.curB, tc.startupRaw, tc.curRaw)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRestartRequired_NilConfigs(t *testing.T) {
	t.Parallel()

	require.Nil(t, RestartRequired(nil, &Config{}, nil, nil, "", ""))
	require.Nil(t, RestartRequired(&Config{}, nil, nil, nil, "", ""))
}

func TestRestartRequired_NilAndEmptyMapsAreEqual(t *testing.T) {
	t.Parallel()

	cur := &Config{MCP: MCPs{}, LSP: LSPs{}}
	require.Empty(t, RestartRequired(&Config{}, cur, nil, nil, "", ""))
}

func TestRestartRequired_NilOptions(t *testing.T) {
	t.Parallel()

	cur := &Config{Options: &Options{TUI: &TUIOptions{CompactMode: true}}}
	require.Equal(t, []string{"options.tui"}, RestartRequired(&Config{}, cur, nil, nil, "", ""))
}

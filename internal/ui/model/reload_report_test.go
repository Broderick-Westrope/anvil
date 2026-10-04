package model

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

func TestFormatReloadReport(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	tests := []struct {
		name    string
		report  workspace.ReloadReport
		want    string
		wantErr bool
	}{
		{
			name: "all ok",
			want: "Config and plugins reloaded",
		},
		{
			name:    "plugins busy",
			report:  workspace.ReloadReport{PluginsBusy: true},
			want:    "Config reloaded · plugins not reloaded: agent is busy",
			wantErr: true,
		},
		{
			name: "plugin warnings",
			report: workspace.ReloadReport{PluginWarnings: []agent.PluginWarning{
				{Path: "/p/a/plugin.json", Err: boom},
				{Path: "/p/b/agents/x.md", Err: boom},
			}},
			want: "Config and plugins reloaded · 2 plugin warnings: /p/a/plugin.json",
		},
		{
			name: "one plugin warning",
			report: workspace.ReloadReport{PluginWarnings: []agent.PluginWarning{
				{Path: "/p/a/plugin.json", Err: boom},
			}},
			want: "Config and plugins reloaded · 1 plugin warning: /p/a/plugin.json",
		},
		{
			name:   "restart pending",
			report: workspace.ReloadReport{RestartRequired: []string{"mcp", "lsp"}},
			want:   "Config and plugins reloaded · mcp, lsp need /reload-instance",
		},
		{
			name:   "one restart pending",
			report: workspace.ReloadReport{RestartRequired: []string{"bouncer"}},
			want:   "Config and plugins reloaded · bouncer needs /reload-instance",
		},
		{
			name:   "bouncer mode only",
			report: workspace.ReloadReport{RestartRequired: []string{"bouncer.mode"}},
			want:   "Config and plugins reloaded · bouncer mode differs from config (ctrl+q)",
		},
		{
			name:   "restart and bouncer mode",
			report: workspace.ReloadReport{RestartRequired: []string{"mcp", "bouncer.mode", "options.tui"}},
			want:   "Config and plugins reloaded · mcp, options.tui need /reload-instance · bouncer mode differs from config (ctrl+q)",
		},
		{
			name:    "config error",
			report:  workspace.ReloadReport{ConfigErr: boom},
			want:    "Config not reloaded: boom · plugins reloaded",
			wantErr: true,
		},
		{
			name:    "plugin error",
			report:  workspace.ReloadReport{PluginsErr: boom},
			want:    "Config reloaded · plugins failed: boom",
			wantErr: true,
		},
		{
			name: "plugin error hides warnings",
			report: workspace.ReloadReport{PluginsErr: boom, PluginWarnings: []agent.PluginWarning{
				{Path: "/p/a/plugin.json", Err: boom},
			}},
			want:    "Config reloaded · plugins failed: boom",
			wantErr: true,
		},
		{
			name:    "apply error",
			report:  workspace.ReloadReport{ApplyErr: boom},
			want:    "Config reloaded but not applied: boom · plugins reloaded",
			wantErr: true,
		},
		{
			name:   "plugins skipped",
			report: workspace.ReloadReport{PluginsSkipped: true},
			want:   "Config reloaded · plugins load once a model is set up",
		},
		{
			name:    "config and plugin errors",
			report:  workspace.ReloadReport{ConfigErr: boom, PluginsErr: errors.New("bad agent")},
			want:    "Config not reloaded: boom · plugins failed: bad agent",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, isErr := formatReloadReport(tc.report)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.wantErr, isErr)
		})
	}
}

func TestFormatReloadReport_TruncatesErrors(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 300)
	got, isErr := formatReloadReport(workspace.ReloadReport{ConfigErr: errors.New(long)})
	require.True(t, isErr)

	prefix := "Config not reloaded: "
	suffix := " · plugins reloaded"
	require.True(t, strings.HasPrefix(got, prefix))
	require.True(t, strings.HasSuffix(got, suffix))
	errPart := strings.TrimSuffix(strings.TrimPrefix(got, prefix), suffix)
	require.Equal(t, reloadErrMaxRunes, utf8.RuneCountInString(errPart))
	require.True(t, strings.HasSuffix(errPart, "…"))
}

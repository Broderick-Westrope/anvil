package agent

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestToolAccessAllowsTool(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		allowed, disabled []string
		tool              string
		want              bool
	}{
		"nil filter":                      {tool: tools.ViewToolName, want: true},
		"wildcard":                        {allowed: []string{"*"}, tool: tools.ViewToolName, want: true},
		"empty filter":                    {allowed: []string{}, tool: tools.ViewToolName},
		"included":                        {allowed: []string{tools.ViewToolName}, tool: tools.ViewToolName, want: true},
		"not included":                    {allowed: []string{tools.BashToolName}, tool: tools.ViewToolName},
		"excluded":                        {allowed: []string{"!" + tools.ViewToolName}, tool: tools.ViewToolName},
		"not excluded":                    {allowed: []string{"!" + tools.BashToolName}, tool: tools.ViewToolName, want: true},
		"invalid filter allows all":       {allowed: []string{tools.BashToolName, "!" + tools.ViewToolName}, tool: tools.ViewToolName, want: true},
		"globally disabled":               {disabled: []string{tools.ViewToolName}, tool: tools.ViewToolName},
		"included but globally disabled":  {allowed: []string{tools.ViewToolName}, disabled: []string{tools.ViewToolName}, tool: tools.ViewToolName},
		"job tool with bash":              {allowed: []string{tools.BashToolName}, tool: tools.JobKillToolName, want: true},
		"job tool without bash":           {allowed: []string{tools.ViewToolName}, tool: tools.JobKillToolName},
		"job tool excluded by name":       {allowed: []string{"!" + tools.JobKillToolName}, tool: tools.JobKillToolName},
		"job tool with bash disabled":     {allowed: []string{tools.BashToolName}, disabled: []string{tools.BashToolName}, tool: tools.JobListToolName, want: true},
		"job tool disabled":               {allowed: []string{tools.BashToolName}, disabled: []string{tools.JobOutputToolName}, tool: tools.JobOutputToolName},
		"other tool does not follow bash": {allowed: []string{tools.BashToolName}, tool: tools.ViewToolName},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			access, _ := newToolAccess(config.Agent{AllowedTools: tt.allowed}, &config.Options{DisabledTools: tt.disabled})
			require.Equal(t, tt.want, access.allowsTool(tt.tool))
		})
	}
}

func TestNewToolAccessReportsInvalidFilter(t *testing.T) {
	t.Parallel()
	_, err := newToolAccess(config.Agent{AllowedTools: []string{"view", "!bash"}}, nil)
	require.Error(t, err)
	access, err := newToolAccess(config.Agent{AllowedTools: []string{"!bash"}}, nil)
	require.NoError(t, err)
	require.False(t, access.allowsTool(tools.BashToolName))
}

func TestToolAccessAllowsMCP(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		allowedMCP map[string][]string
		wantTool   bool
		wantServer bool
	}{
		"no restrictions": {wantTool: true, wantServer: true},
		"none allowed":    {allowedMCP: map[string][]string{}},
		"whole server":    {allowedMCP: map[string][]string{"datadog": nil}, wantTool: true, wantServer: true},
		"listed tool":     {allowedMCP: map[string][]string{"datadog": {"query"}}, wantTool: true, wantServer: true},
		"other tool":      {allowedMCP: map[string][]string{"datadog": {"other"}}, wantServer: true},
		"other server":    {allowedMCP: map[string][]string{"sentry": nil}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			access, err := newToolAccess(config.Agent{AllowedMCP: tt.allowedMCP}, nil)
			require.NoError(t, err)
			require.Equal(t, tt.wantTool, access.allowsMCPTool("datadog", "query"))
			require.Equal(t, tt.wantServer, access.allowsMCPServer("datadog"))
		})
	}
}

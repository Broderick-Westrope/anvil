package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

type namedTool struct {
	fantasy.AgentTool
	name string
}

func (n namedTool) Info() fantasy.ToolInfo { return fantasy.ToolInfo{Name: n.name} }

func TestRepairToolCall(t *testing.T) {
	t.Parallel()
	withCall := []fantasy.AgentTool{namedTool{name: tools.CallMCPToolToolName}}
	tests := map[string]struct {
		call      fantasy.ToolCallContent
		available []fantasy.AgentTool
		want      *fantasy.ToolCallContent
	}{
		"direct name forwards to the longest matching server": {
			call:      fantasy.ToolCallContent{ToolCallID: "1", ToolName: "mcp_Data_dog_search", Input: `{"q":1}`},
			available: withCall,
			want:      &fantasy.ToolCallContent{ToolCallID: "1", ToolName: tools.CallMCPToolToolName, Input: `{"server":"data_dog","tool":"search","arguments":{"q":1}}`},
		},
		"no call_mcp_tool to forward to": {
			call: fantasy.ToolCallContent{ToolName: "mcp_data_dog_search", Input: `{}`},
		},
		"not a catalogue server": {
			call:      fantasy.ToolCallContent{ToolName: "mcp_other_search", Input: `{}`},
			available: withCall,
		},
		"a listed tool that failed validation is left alone": {
			call:      fantasy.ToolCallContent{ToolName: "mcp_data_dog_search", Input: `{}`},
			available: append([]fantasy.AgentTool{namedTool{name: "mcp_data_dog_search"}}, withCall...),
		},
		"a longer direct server wins over a catalogue prefix": {
			call:      fantasy.ToolCallContent{ToolName: "mcp_data_dog_x_search", Input: `{}`},
			available: withCall,
		},
		"malformed JSON in a direct name is repaired, then forwarded": {
			call:      fantasy.ToolCallContent{ToolName: "mcp_data_dog_search", Input: `{"q":1`},
			available: withCall,
			want:      &fantasy.ToolCallContent{ToolName: tools.CallMCPToolToolName, Input: `{"server":"data_dog","tool":"search","arguments":{"q":1}}`},
		},
		"empty input forwards as an empty object": {
			call:      fantasy.ToolCallContent{ToolName: "mcp_data_dog_whoami", Input: ``},
			available: withCall,
			want:      &fantasy.ToolCallContent{ToolName: tools.CallMCPToolToolName, Input: `{"server":"data_dog","tool":"whoami","arguments":{}}`},
		},
		"malformed JSON is repaired as before": {
			call:      fantasy.ToolCallContent{ToolName: "view", Input: `{"path":"a"`},
			available: withCall,
			want:      &fantasy.ToolCallContent{ToolName: "view", Input: `{"path": "a"}`},
		},
	}
	repair := repairToolCall(mcpListing{
		catalogue: map[string]bool{"data": true, "data_dog": true},
		routed:    []string{"data", "data_dog", "data_dog_x"},
	})
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := repair(t.Context(), fantasy.ToolCallRepairOptions{OriginalToolCall: tc.call, AvailableTools: tc.available})
			if tc.want == nil {
				require.ErrorIs(t, err, errNoRepair)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRepairPrefersTheServerWhoseCaseMatches(t *testing.T) {
	t.Parallel()
	repair := repairToolCall(mcpListing{catalogue: map[string]bool{"Jira": true, "jira": true}, routed: []string{"Jira", "jira"}})

	got, err := repair(t.Context(), fantasy.ToolCallRepairOptions{
		OriginalToolCall: fantasy.ToolCallContent{ToolName: "mcp_jira_get", Input: `{}`},
		AvailableTools:   []fantasy.AgentTool{namedTool{name: tools.CallMCPToolToolName}},
	})

	require.NoError(t, err)
	require.Equal(t, `{"server":"jira","tool":"get","arguments":{}}`, got.Input)
}

func TestRepairForwardsToTheLongestMatchingServer(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		routed []string
		name   string
		want   string
	}{
		"a server that doesn't match is skipped": {
			routed: []string{"alpha", "beta"}, name: "mcp_beta_get", want: `{"server":"beta","tool":"get","arguments":{}}`,
		},
		"a name no longer than a prefix doesn't match it": {
			routed: []string{"data", "data_dog"}, name: "mcp_data_dog_", want: `{"server":"data","tool":"dog_","arguments":{}}`,
		},
		"a longer server wins over a shorter one whose case matches": {
			routed: []string{"Data_dog", "data"}, name: "mcp_data_dog_search", want: `{"server":"Data_dog","tool":"search","arguments":{}}`,
		},
		"at equal length the case-exact server wins when listed first": {
			routed: []string{"JIRA", "jira"}, name: "mcp_JIRA_get", want: `{"server":"JIRA","tool":"get","arguments":{}}`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			catalogue := map[string]bool{}
			for _, server := range tc.routed {
				catalogue[server] = true
			}
			repair := repairToolCall(mcpListing{catalogue: catalogue, routed: tc.routed})

			got, err := repair(t.Context(), fantasy.ToolCallRepairOptions{
				OriginalToolCall: fantasy.ToolCallContent{ToolName: tc.name, Input: `{}`},
				AvailableTools:   []fantasy.AgentTool{namedTool{name: tools.CallMCPToolToolName}},
			})

			require.NoError(t, err)
			require.Equal(t, tc.want, got.Input)
		})
	}
}

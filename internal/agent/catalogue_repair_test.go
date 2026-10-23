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
		"malformed JSON is repaired as before": {
			call:      fantasy.ToolCallContent{ToolName: "view", Input: `{"path":"a"`},
			available: withCall,
			want:      &fantasy.ToolCallContent{ToolName: "view", Input: `{"path": "a"}`},
		},
	}
	repair := repairToolCall(map[string]bool{"data": true, "data_dog": true})
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

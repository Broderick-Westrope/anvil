package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/jsonrepair"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
)

var errNoRepair = errors.New("no repair")

// repairToolCall forwards a call to a catalogue server's tool by its
// direct name (mcp_<server>_<tool>, say from a resumed session's history,
// or a server routed direct in an earlier process) to call_mcp_tool, which
// validates and runs it as the inner tool. Any other invalid call gets
// fantasy's default repair of malformed JSON, which a custom repair
// function replaces.
func repairToolCall(catalogue map[string]bool) fantasy.RepairToolCallFunction {
	servers := slices.Sorted(maps.Keys(catalogue))
	return func(_ context.Context, opts fantasy.ToolCallRepairOptions) (*fantasy.ToolCallContent, error) {
		call := opts.OriginalToolCall
		if forwarded, ok := forwardToCatalogue(call, servers, opts.AvailableTools); ok {
			return &forwarded, nil
		}
		repaired, err := jsonrepair.RepairJSON(call.Input)
		if err != nil || repaired == call.Input {
			return nil, errNoRepair
		}
		call.Input = repaired
		return &call, nil
	}
}

func forwardToCatalogue(call fantasy.ToolCallContent, servers []string, available []fantasy.AgentTool) (fantasy.ToolCallContent, bool) {
	if !slices.ContainsFunc(available, func(t fantasy.AgentTool) bool { return t.Info().Name == tools.CallMCPToolToolName }) {
		return call, false
	}
	server := ""
	for _, candidate := range servers {
		// The longest matching server wins: names can contain "_". The
		// match ignores case (the OAuth rename capitalises the server)
		// over the prefix's own bytes, so slicing the tool off is safe.
		prefix := "mcp_" + candidate + "_"
		if len(call.ToolName) > len(prefix) && strings.EqualFold(call.ToolName[:len(prefix)], prefix) && len(candidate) > len(server) {
			server = candidate
		}
	}
	if server == "" {
		return call, false
	}
	arguments := json.RawMessage(call.Input)
	if !json.Valid(arguments) {
		return call, false
	}
	input, err := json.Marshal(struct {
		Server    string          `json:"server"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}{server, call.ToolName[len("mcp_"+server+"_"):], arguments})
	if err != nil {
		return call, false
	}
	call.ToolName = tools.CallMCPToolToolName
	call.Input = string(input)
	return call, true
}

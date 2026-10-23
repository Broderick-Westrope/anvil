package agent

import (
	"context"
	"encoding/json"
	"errors"
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
// validates and runs it as the inner tool. Malformed JSON is repaired
// first, as fantasy's default repair (which a custom function replaces)
// does for every call.
func repairToolCall(listing mcpListing) fantasy.RepairToolCallFunction {
	return func(_ context.Context, opts fantasy.ToolCallRepairOptions) (*fantasy.ToolCallContent, error) {
		call := opts.OriginalToolCall
		repaired := call
		if fixed, err := jsonrepair.RepairJSON(call.Input); err == nil {
			repaired.Input = fixed
		}
		if forwarded, ok := forwardToCatalogue(repaired, listing, opts.AvailableTools); ok {
			return &forwarded, nil
		}
		if repaired.Input == call.Input {
			return nil, errNoRepair
		}
		return &repaired, nil
	}
}

func forwardToCatalogue(call fantasy.ToolCallContent, listing mcpListing, available []fantasy.AgentTool) (fantasy.ToolCallContent, bool) {
	hasTool := func(name string) bool {
		return slices.ContainsFunc(available, func(t fantasy.AgentTool) bool { return t.Info().Name == name })
	}
	// A listed tool failed validation for its own reasons; it isn't a
	// catalogue tool called by name.
	if hasTool(call.ToolName) || !hasTool(tools.CallMCPToolToolName) {
		return call, false
	}
	server := longestServerPrefix(call.ToolName, listing.routed)
	arguments := call.Input
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	if !listing.catalogue[server] || !json.Valid([]byte(arguments)) {
		return call, false
	}
	input, err := json.Marshal(struct {
		Server    string          `json:"server"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}{server, call.ToolName[len("mcp_"+server+"_"):], json.RawMessage(arguments)})
	if err != nil {
		return call, false
	}
	call.ToolName = tools.CallMCPToolToolName
	call.Input = string(input)
	return call, true
}

// longestServerPrefix returns the server whose mcp_<server>_ prefix name
// starts with, preferring the longest since server names can contain "_",
// or "". The match ignores case (the OAuth rename capitalises the server)
// over the prefix's own bytes, so slicing the tool name off is safe.
func longestServerPrefix(name string, servers []string) string {
	best := ""
	for _, server := range servers {
		prefix := "mcp_" + server + "_"
		if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) && len(server) > len(best) {
			best = server
		}
	}
	return best
}

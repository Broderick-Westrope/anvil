package agent

import (
	"maps"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
)

// mcpInstructions joins the instructions of connected MCP servers in name
// order, leaving out catalogue servers (search results carry theirs). The
// order is fixed so the system prompt is identical across runs and its
// prompt cache stays valid.
func mcpInstructions(listing mcpListing) string {
	states := mcp.GetStates()
	var instructions strings.Builder
	for _, name := range slices.Sorted(maps.Keys(states)) {
		server := states[name]
		if server.State != mcp.StateConnected {
			continue
		}
		if listing.catalogue[name] && !listing.named[name] {
			continue
		}
		if s := server.Client.InitializeResult().Instructions; s != "" {
			instructions.WriteString(s)
			instructions.WriteString("\n\n")
		}
	}
	return instructions.String()
}

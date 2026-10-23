package tools

// MCPRoute is how one MCP server's tools reach the model, for display in
// anvil_info and the MCP palette.
type MCPRoute struct {
	Server    string
	Catalogue bool
	// Reason says why, e.g. "auto: lazy" or "route: direct".
	Reason string
}

// String describes the route and its reason, e.g.
// "catalogue (auto: lazy)".
func (r MCPRoute) String() string {
	kind := "direct"
	if r.Catalogue {
		kind = "catalogue"
	}
	return kind + " (" + r.Reason + ")"
}

// MCPRoutesFunc returns every routed server's route in name order, and
// false while the routes are still pending (MCP initialisation hasn't
// finished).
type MCPRoutesFunc func() ([]MCPRoute, bool)

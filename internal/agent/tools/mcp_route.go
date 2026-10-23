package tools

// MCPRoute is how one MCP server's tools reach the model, for display in
// anvil_info and the MCP palette.
type MCPRoute struct {
	Server    string
	Catalogue bool
	// Reason says why, e.g. "auto: lazy" or "route: direct".
	Reason string
	// Short is the reason in a few words, e.g. "lazy" or "10,190 B".
	Short string
}

// String describes the route and its reason, e.g.
// "catalogue (auto: lazy)".
func (r MCPRoute) String() string {
	return r.kind() + " (" + r.Reason + ")"
}

// ShortString is String with the short reason, e.g. "catalogue (lazy)".
func (r MCPRoute) ShortString() string {
	return r.kind() + " (" + r.Short + ")"
}

func (r MCPRoute) kind() string {
	if r.Catalogue {
		return "catalogue"
	}
	return "direct"
}

// MCPRoutesFunc returns every routed server's route in name order, and
// false while the routes are still pending (MCP initialisation hasn't
// finished).
type MCPRoutesFunc func() ([]MCPRoute, bool)

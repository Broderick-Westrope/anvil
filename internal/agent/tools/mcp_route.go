package tools

// MCPRouteInfo is how one MCP server's tools reach the model, for display in
// anvil_info and the MCP palette.
type MCPRouteInfo struct {
	Server    string
	Catalogue bool
	// Reason says why, e.g. "auto: lazy" or "route: direct".
	Reason string
	// Short is the reason in a few words, e.g. "lazy" or "under 12 kB".
	Short string
}

// String describes the route and its reason, e.g.
// "catalogue (auto: lazy)".
func (r MCPRouteInfo) String() string {
	return r.kind() + " (" + r.Reason + ")"
}

// ShortString is String with the short reason, e.g. "catalogue (lazy)".
func (r MCPRouteInfo) ShortString() string {
	return r.kind() + " (" + r.Short + ")"
}

func (r MCPRouteInfo) kind() string {
	if r.Catalogue {
		return "catalogue"
	}
	return "direct"
}

// MCPRoutesFunc returns every routed server's route in name order, and
// false while the routes are still pending (MCP initialisation hasn't
// finished).
type MCPRoutesFunc func() ([]MCPRouteInfo, bool)

package agent

import (
	"fmt"
	"maps"
	"slices"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/dustin/go-humanize"
)

// catalogueThresholdBytes is the size of a server's sent tools at which
// auto routing stops listing them directly. Listing costs the schema on
// every step; the catalogue costs about two extra steps in sessions that
// use the server. 12,000 bytes is about 5k tokens, which keeps small
// servers fast and sends every large one through the catalogue. See
// plans/completed/design-2026-10-23-mcp-tool-catalogue.md.
const catalogueThresholdBytes = 12_000

// mcpRoute is how one server's tools reach the model for the rest of the
// process, and why.
type mcpRoute struct {
	catalogue bool
	// auto is set when the size rule chose the route.
	auto   bool
	reason string
	// description introduces the server in the catalogue tools: its
	// lazy_description, else its name. Never connect state, so it can't
	// change when the server connects later.
	description string
}

// mcpRoutes maps each server in the startup snapshot to its route. It is
// derived once per process and never written afterwards, so the tool
// list and catalogue descriptions stay fixed. A server missing from it
// (disabled in config at startup, Docker MCP, added by a reload) keeps
// today's behaviour: listed directly once connected.
type mcpRoutes map[string]mcpRoute

// deriveMCPRoutes applies the route rule to every enabled server in the
// snapshot.
func deriveMCPRoutes(snap map[string]toolsmcp.ServerSnapshot) mcpRoutes {
	routes := mcpRoutes{}
	for name, server := range snap {
		if server.Config.Disabled {
			continue
		}
		catalogue, reason := routeFor(server)
		description := server.Config.LazyDescription
		if description == "" {
			description = name
		}
		auto := server.Config.Route != config.MCPRouteDirect && server.Config.Route != config.MCPRouteCatalogue
		routes[name] = mcpRoute{catalogue: catalogue, auto: auto, reason: reason, description: description}
	}
	return routes
}

func routeFor(server toolsmcp.ServerSnapshot) (catalogue bool, reason string) {
	switch server.Config.Route {
	case config.MCPRouteDirect:
		return false, "route: direct"
	case config.MCPRouteCatalogue:
		return true, "route: catalogue"
	}
	if server.Config.IsLazy() {
		return true, "auto: lazy"
	}
	if server.State != toolsmcp.StateConnected {
		return true, fmt.Sprintf("auto: %s at startup", server.State)
	}
	size := humanize.Comma(int64(server.SentBytes))
	if server.SentBytes < catalogueThresholdBytes {
		return false, fmt.Sprintf("auto: %s bytes, under %s", size, humanize.Comma(catalogueThresholdBytes))
	}
	return true, fmt.Sprintf("auto: %s bytes, not under %s", size, humanize.Comma(catalogueThresholdBytes))
}

// isCatalogue reports whether the server's tools go through the
// catalogue.
func (r mcpRoutes) isCatalogue(server string) bool {
	return r[server].catalogue
}

// catalogueServers returns the catalogue-routed servers in name order.
func (r mcpRoutes) catalogueServers() []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(r)) {
		if r[name].catalogue {
			names = append(names, name)
		}
	}
	return names
}

// MCPRoutes returns every routed server's route for display, flagging an
// auto-direct server whose tools have since grown past the threshold: it
// stays direct until restart, so the tool list can't change mid-process.
func (c *coordinator) MCPRoutes() ([]tools.MCPRoute, bool) {
	routes, ready := c.mcpRoutes()
	if !ready {
		return nil, false
	}
	var out []tools.MCPRoute
	for _, name := range slices.Sorted(maps.Keys(routes)) {
		route := routes[name]
		reason := route.reason
		if size := toolsmcp.SentToolBytes(name); route.auto && !route.catalogue && size >= catalogueThresholdBytes {
			reason += fmt.Sprintf("; now %s bytes, past %s, so it moves to the catalogue at the next restart",
				humanize.Comma(int64(size)), humanize.Comma(catalogueThresholdBytes))
		}
		out = append(out, tools.MCPRoute{Server: name, Catalogue: route.catalogue, Reason: reason})
	}
	return out, true
}

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
// servers fast and sends every large one through the catalogue.
const catalogueThresholdBytes = 12_000

// mcpRoute is how one server's tools reach the model for the rest of the
// process, and why.
type mcpRoute struct {
	catalogue bool
	// auto is set when the size rule chose the route.
	auto   bool
	reason string
	// short is the reason in a few words, for the MCP palette.
	short string
	// description introduces the server in the catalogue tools: its
	// lazy_description, else its name. Never connect state, so it can't
	// change when the server connects later.
	description string
}

// mcpRoutes maps each server in the startup snapshot to its route. It is
// derived once per process and never written afterwards, so the tool
// list and catalogue descriptions stay fixed. A server missing from it
// (disabled in config at startup, Docker MCP, added by a reload) is
// listed directly once connected.
type mcpRoutes map[string]mcpRoute

// deriveMCPRoutes applies the route rule to every enabled server in the
// snapshot except Docker MCP, which is listed directly however it was
// enabled.
func deriveMCPRoutes(snap map[string]toolsmcp.ServerSnapshot) mcpRoutes {
	routes := mcpRoutes{}
	for name, server := range snap {
		if server.Config.Disabled || name == config.DockerMCPName {
			continue
		}
		catalogue, reason, short := routeFor(server)
		description := server.Config.LazyDescription
		if description == "" {
			description = name
		}
		auto := server.Config.Route != config.MCPRouteDirect && server.Config.Route != config.MCPRouteCatalogue
		routes[name] = mcpRoute{catalogue: catalogue, auto: auto, reason: reason, short: short, description: description}
	}
	return routes
}

func routeFor(server toolsmcp.ServerSnapshot) (catalogue bool, reason, short string) {
	switch server.Config.Route {
	case config.MCPRouteDirect:
		return false, "route: direct", "set in config"
	case config.MCPRouteCatalogue:
		return true, "route: catalogue", "set in config"
	}
	if server.Config.IsLazy() {
		return true, "auto: lazy", "lazy"
	}
	if server.State != toolsmcp.StateConnected {
		return true, fmt.Sprintf("auto: %s at startup", server.State), fmt.Sprintf("%s at startup", server.State)
	}
	size := humanize.Comma(int64(server.SentBytes))
	short = humanize.Bytes(uint64(server.SentBytes))
	if server.SentBytes < catalogueThresholdBytes {
		return false, fmt.Sprintf("auto: %s bytes, under %s", size, humanize.Comma(catalogueThresholdBytes)), short
	}
	return true, fmt.Sprintf("auto: %s bytes, not under %s", size, humanize.Comma(catalogueThresholdBytes)), short
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

// mcpRoutes returns the route map, deriving it on the first call after
// the startup snapshot exists. It reports false before then.
func (c *coordinator) mcpRoutes() (mcpRoutes, bool) {
	c.routesMu.Lock()
	defer c.routesMu.Unlock()
	if c.routes != nil {
		return c.routes, true
	}
	snap, ready := toolsmcp.StartupSnapshot(c.cfg)
	if !ready {
		return nil, false
	}
	c.routes = deriveMCPRoutes(snap)
	return c.routes, true
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
		reason, short := route.reason, route.short
		if size := toolsmcp.SentToolBytes(name); route.auto && !route.catalogue && size >= catalogueThresholdBytes {
			now := humanize.Comma(int64(size))
			reason += fmt.Sprintf("; now %s bytes, past %s, so it moves to the catalogue at the next restart",
				now, humanize.Comma(catalogueThresholdBytes))
			short += ", now " + humanize.Bytes(uint64(size))
		}
		out = append(out, tools.MCPRoute{Server: name, Catalogue: route.catalogue, Reason: reason, Short: short})
	}
	return out, true
}

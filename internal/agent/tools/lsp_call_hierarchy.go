package tools

import (
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

type CallHierarchyParams struct {
	Symbol    string `json:"symbol" description:"The symbol name to show call hierarchy for"`
	Direction string `json:"direction" description:"Either 'incoming' (who calls this) or 'outgoing' (what does this call)"`
	Path      string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
}

const CallHierarchyToolName = "lsp_call_hierarchy"

//go:embed lsp_call_hierarchy.md
var callHierarchyDescription string

func NewCallHierarchyTool(lspManager *lsp.Manager) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		CallHierarchyToolName,
		callHierarchyDescription,
		func(ctx context.Context, params CallHierarchyParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Symbol == "" {
				return fantasy.NewTextErrorResponse("symbol is required"), nil
			}
			direction, ok := callDirections[params.Direction]
			if !ok {
				return fantasy.NewTextErrorResponse("direction must be 'incoming' or 'outgoing'"), nil
			}
			workingDir := cmp.Or(params.Path, ".")
			resolved, err := resolveSymbol(ctx, lspManager, params.Symbol, workingDir)
			if err != nil {
				return fantasy.NewTextResponse(fmt.Sprintf("Symbol '%s' not found", params.Symbol)), nil
			}

			items, err := resolved.client.PrepareCallHierarchy(ctx, resolved.path, resolved.line, resolved.char)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to prepare call hierarchy: %s", err)), nil
			}
			if len(items) == 0 {
				return fantasy.NewTextResponse(fmt.Sprintf("No call hierarchy information for '%s'", params.Symbol)), nil
			}

			item := items[0]

			var b strings.Builder
			fmt.Fprintf(&b, "Call hierarchy for '%s':\n\n", item.Name)

			calls, err := direction.calls(ctx, resolved.client, item)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to get %s calls: %s", params.Direction, err)), nil
			}
			if len(calls) == 0 {
				fmt.Fprintf(&b, "No %s calls found.\n", params.Direction)
			} else {
				fmt.Fprintf(&b, "%d %s:\n\n", len(calls), direction.noun)
				for _, c := range calls {
					path, _ := c.URI.Path()
					line := c.Range.Start.Line + 1
					fmt.Fprintf(&b, "  %s:%d — %s\n", path, line, c.Name)
				}
			}

			return fantasy.NewTextResponse(b.String()), nil
		},
	)
}

// callDirection is one direction lsp_call_hierarchy can list calls in.
type callDirection struct {
	noun string
	// calls returns the item at the other end of each call.
	calls func(ctx context.Context, client *lsp.Client, item protocol.CallHierarchyItem) ([]protocol.CallHierarchyItem, error)
}

var callDirections = map[string]callDirection{
	"incoming": {noun: "caller(s)", calls: callers},
	"outgoing": {noun: "callee(s)", calls: callees},
}

func callers(ctx context.Context, client *lsp.Client, item protocol.CallHierarchyItem) ([]protocol.CallHierarchyItem, error) {
	calls, err := client.IncomingCalls(ctx, item)
	if err != nil {
		return nil, err
	}
	items := make([]protocol.CallHierarchyItem, len(calls))
	for i, c := range calls {
		items[i] = c.From
	}
	return items, nil
}

func callees(ctx context.Context, client *lsp.Client, item protocol.CallHierarchyItem) ([]protocol.CallHierarchyItem, error) {
	calls, err := client.OutgoingCalls(ctx, item)
	if err != nil {
		return nil, err
	}
	items := make([]protocol.CallHierarchyItem, len(calls))
	for i, c := range calls {
		items[i] = c.To
	}
	return items, nil
}

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/fantasy"
	"github.com/google/jsonschema-go/jsonschema"
)

// CatalogueCall is a call_mcp_tool input: the inner tool and the
// arguments it runs with. Every reader and writer of that input goes
// through it, so the tool, the chat and direct-name forwarding agree.
type CatalogueCall struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// ParseCatalogueCall reads a call_mcp_tool input. Arguments may be a JSON
// value or a string holding one, since models often send nested objects
// as strings; missing or null arguments are an empty object. It doesn't
// check that the arguments are an object.
func ParseCatalogueCall(input string) (CatalogueCall, error) {
	var call CatalogueCall
	if err := json.Unmarshal([]byte(input), &call); err != nil {
		return CatalogueCall{}, fmt.Errorf("invalid input: %w", err)
	}
	var asString string
	if json.Unmarshal(call.Arguments, &asString) == nil {
		call.Arguments = json.RawMessage(asString)
	}
	if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
		call.Arguments = json.RawMessage("{}")
	}
	return call, nil
}

// Input encodes the call as call_mcp_tool's input. Arguments must be
// valid JSON.
func (c CatalogueCall) Input() string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (c *catalogue) call(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	params, err := ParseCatalogueCall(call.Input)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	params.Server = c.canonicalServer(params.Server)
	instructions, tools, err := c.serverTools(ctx, params.Server, CallMCPToolToolName)
	if err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	tool, ok := findTool(params.Server, params.Tool, tools)
	if !ok {
		return fantasy.NewTextErrorResponse(unknownToolMessage(params.Server, params.Tool, tools)), nil
	}
	args := []byte(params.Arguments)
	problem := ""
	var object map[string]any
	if json.Unmarshal(args, &object) != nil {
		problem = "arguments must be a JSON object"
	} else {
		problem = validateArguments(tool.MCP, args)
	}
	if problem != "" {
		marker := ""
		if mentionIndex(instructions, tool.MCP.MCPToolName()) >= 0 {
			marker = InstructionNamedMarker
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("%s%s: %s\n\nInput schema for %s:\n%s",
			InvalidArgumentsPrefix, marker, problem, tool.MCP.MCPToolName(), compactSchema(tool.MCP))), nil
	}
	return tool.Run.Run(ctx, fantasy.ToolCall{ID: call.ID, Name: tool.Run.Info().Name, Input: string(args)})
}

func unknownToolMessage(server, name string, tools []CatalogueTool) string {
	matches := rankTools(searchTerms(name), map[string][]CatalogueTool{server: tools})
	if len(matches) == 0 {
		return fmt.Sprintf("no tool %q on %s. Use search_mcp_tools to find it.", name, server)
	}
	var names []string
	for _, match := range matches[:min(len(matches), maxClosestMatches)] {
		names = append(names, match.tool.MCPToolName())
	}
	return fmt.Sprintf("no tool %q on %s. Closest matches: %s", name, server, strings.Join(names, ", "))
}

// validateArguments checks args against the tool's input schema and
// describes any violations. A schema jsonschema-go can't load (it won't
// unmarshal, or has a remote $ref) passes every call through.
func validateArguments(tool *Tool, args []byte) string {
	raw, err := json.Marshal(tool.InputSchema())
	if err != nil {
		return ""
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		slog.Debug("MCP tool schema won't load; passing call through unvalidated", "tool", tool.Name(), "error", err)
		return ""
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		slog.Debug("MCP tool schema won't resolve; passing call through unvalidated", "tool", tool.Name(), "error", err)
		return ""
	}
	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		return "arguments are not valid JSON: " + err.Error()
	}
	if err := resolved.Validate(value); err != nil {
		return err.Error()
	}
	return ""
}

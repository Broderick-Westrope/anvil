package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/secrets"
)

// guardedFields are the input fields, per tool, where a placeholder would
// quietly do harm: written to disk it replaces the real secret, and in a
// command it is either about to be written somewhere or breaks the
// command. Elsewhere (a path, a description, an MCP call) a placeholder
// leaks nothing.
var guardedFields = map[string][]string{
	tools.WriteToolName:     {"content"},
	tools.EditToolName:      {"old_string", "new_string"},
	tools.MultiEditToolName: {"edits"},
	tools.BashToolName:      {"command"},
}

// guardSecretPlaceholders wraps the guarded tools so a call holding a
// placeholder the detector issued is refused before hooks, permission
// prompts or the tool run. A nil detector (masking off) leaves tools
// alone.
func guardSecretPlaceholders(agentTools []fantasy.AgentTool, detector *secrets.Detector) []fantasy.AgentTool {
	if detector == nil {
		return agentTools
	}
	guarded := make([]fantasy.AgentTool, len(agentTools))
	for i, tool := range agentTools {
		if fields, ok := guardedFields[tool.Info().Name]; ok {
			tool = &secretGuardTool{AgentTool: tool, detector: detector, fields: fields}
		}
		guarded[i] = tool
	}
	return guarded
}

type secretGuardTool struct {
	fantasy.AgentTool
	detector *secrets.Detector
	fields   []string
}

func (g *secretGuardTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	placeholders := g.detector.Issued(g.guardedText(call.Input))
	if len(placeholders) == 0 {
		return g.AgentTool.Run(ctx, call)
	}
	return fantasy.NewTextErrorResponse(refusal(call.Name, placeholders)), nil
}

// guardedText returns the decoded strings in the guarded fields of a tool
// call's input, so a JSON escape can't hide a placeholder, or the whole
// input when it isn't a JSON object, so nothing unparsed slips by.
func (g *secretGuardTool) guardedText(input string) string {
	var fields map[string]any
	err := json.Unmarshal([]byte(input), &fields)
	if err != nil {
		return input
	}
	var b strings.Builder
	for _, name := range g.fields {
		writeStrings(&b, fields[name])
	}
	return b.String()
}

// writeStrings writes every string in a decoded JSON value to b.
func writeStrings(b *strings.Builder, value any) {
	switch v := value.(type) {
	case string:
		b.WriteString(v)
		b.WriteByte('\n')
	case []any:
		for _, item := range v {
			writeStrings(b, item)
		}
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			writeStrings(b, v[k])
		}
	}
}

func refusal(tool string, placeholders []string) string {
	var why, ways string
	switch tool {
	case tools.BashToolName:
		why = "In a command it would be passed on literally, not as the value."
		ways = "Read the value from the environment instead (such as $STRIPE_KEY, or a .env the program loads), or ask the user to run the command."
	case tools.EditToolName, tools.MultiEditToolName:
		why = "Written to a file it would replace the real value, and in an old_string it can't match the file."
		ways = "Edit around the secret, read it from the environment (such as $STRIPE_KEY, or a .env the program loads), replace the whole line by its number with a line editor the system has, or ask the user to make the change."
	default:
		why = "Written to a file it would replace the real value."
		ways = "Write a reference to the environment instead, such as $STRIPE_KEY or a .env the program loads, or ask the user to write the value."
	}
	return fmt.Sprintf("%s %s, which stands for a credential Anvil hid from you. %s %s",
		secrets.GuardRefusal, strings.Join(placeholders, ", "), why, ways)
}

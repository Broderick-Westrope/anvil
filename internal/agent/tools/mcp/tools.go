package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"

	"charm.land/fantasy/schema"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Tool = mcp.Tool

// ToolResult represents the result of running an MCP tool.
type ToolResult struct {
	Type      string
	Content   string
	Data      []byte
	MediaType string
}

// RegisteredTool is a server's tool as held in the registry: the tool the
// server listed, untouched, and the parameters sent to the model.
//
// Parameters and Required are a private deep copy of the input schema's
// properties and required list, already normalised the way fantasy
// normalises every tool it sends. fantasy normalises the map it is given
// in place, so normalising it here first leaves fantasy nothing to write:
// the map is shared by every agent's wrapper and stays read-only.
type RegisteredTool struct {
	*Tool
	Parameters map[string]any
	Required   []string
}

var allTools = csync.NewMap[string, []*RegisteredTool]()

// Tools returns all available MCP tools.
func Tools() iter.Seq2[string, []*RegisteredTool] {
	return allTools.Seq2()
}

// ServerTools returns the registered tools of the named server, or nil.
func ServerTools(name string) []*RegisteredTool {
	tools, _ := allTools.Get(name)
	return tools
}

// ToolName is the name the model sees for a server's tool:
// mcp_<server>_<tool>, with the server capitalised under the Anthropic
// OAuth rename.
func ToolName(server, tool string) string {
	if OAuthRenameEnabled() {
		return OAuthToolName(server, tool)
	}
	return fmt.Sprintf("mcp_%s_%s", server, tool)
}

// SentToolBytes is the size of the named server's registered tools as the
// model receives them: the compact JSON of each tool's name, description,
// parameters and required list. Fields the model never sees (_meta,
// annotations, outputSchema, title) don't count. It is 0 for a server with
// no registered tools.
func SentToolBytes(name string) int {
	total := 0
	for _, tool := range ServerTools(name) {
		sent, err := json.Marshal(struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
			Required    []string       `json:"required"`
		}{fmt.Sprintf("mcp_%s_%s", name, tool.Name), tool.Description, tool.Parameters, tool.Required})
		if err != nil {
			slog.Debug("Failed to measure MCP tool", "name", name, "tool", tool.Name, "error", err)
			continue
		}
		total += len(sent)
	}
	return total
}

// newRegisteredTool extracts the parameters fantasy sends for tool and
// normalises a deep copy of them.
func newRegisteredTool(tool *Tool) *RegisteredTool {
	registered := &RegisteredTool{Tool: tool, Parameters: map[string]any{}, Required: []string{}}
	input, ok := tool.InputSchema.(map[string]any)
	if !ok {
		return registered
	}
	if props, ok := input["properties"].(map[string]any); ok {
		registered.Parameters = deepCopy(props)
	}
	switch req := input["required"].(type) {
	case []any:
		for _, v := range req {
			if s, ok := v.(string); ok {
				registered.Required = append(registered.Required, s)
			}
		}
	case []string:
		registered.Required = slices.Clone(req)
	}
	schema.Normalize(map[string]any{
		"type":       "object",
		"properties": registered.Parameters,
		"required":   registered.Required,
	})
	return registered
}

// deepCopy copies a decoded JSON object through a JSON round trip, so the
// copy shares no maps or slices with the original.
func deepCopy(props map[string]any) map[string]any {
	raw, err := json.Marshal(props)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// RunTool runs an MCP tool with the given input parameters.
func RunTool(ctx context.Context, cfg *config.ConfigStore, name, toolName string, input string) (ToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return ToolResult{}, fmt.Errorf("error parsing parameters: %s", err)
	}

	c, err := getOrRenewClient(ctx, cfg, name)
	if err != nil {
		return ToolResult{}, err
	}
	result, err := c.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return ToolResult{}, err
	}

	if len(result.Content) == 0 {
		return ToolResult{Type: "text", Content: ""}, nil
	}

	var textParts []string
	var imageData []byte
	var imageMimeType string
	var audioData []byte
	var audioMimeType string

	for _, v := range result.Content {
		switch content := v.(type) {
		case *mcp.TextContent:
			textParts = append(textParts, content.Text)
		case *mcp.ImageContent:
			if imageData == nil {
				imageData = content.Data
				imageMimeType = content.MIMEType
			}
		case *mcp.AudioContent:
			if audioData == nil {
				audioData = content.Data
				audioMimeType = content.MIMEType
			}
		default:
			textParts = append(textParts, fmt.Sprintf("%v", v))
		}
	}

	textContent := strings.Join(textParts, "\n")

	// We need to make sure the data is base64
	// when using something like docker + playwright the data was not returned correctly.
	if imageData != nil {
		return ToolResult{
			Type:      "image",
			Content:   textContent,
			Data:      ensureRawBytes(imageData),
			MediaType: imageMimeType,
		}, nil
	}

	if audioData != nil {
		return ToolResult{
			Type:      "media",
			Content:   textContent,
			Data:      ensureRawBytes(audioData),
			MediaType: audioMimeType,
		}, nil
	}

	return ToolResult{
		Type:    "text",
		Content: textContent,
	}, nil
}

// RefreshTools gets the updated list of tools from the MCP and updates the
// global state.
func RefreshTools(ctx context.Context, cfg *config.ConfigStore, name string) {
	// Serialize with session renewal so the registered session can't be
	// swapped between the Get and the state update below — a stale error
	// transition would otherwise tear down the healthy replacement.
	mu := renewLock(name)
	mu.Lock()
	defer mu.Unlock()

	session, ok := sessions.Get(name)
	if !ok {
		slog.Warn("Refresh tools: no session", "name", name)
		return
	}

	tools, err := getTools(ctx, session)
	if err != nil {
		updateState(name, StateError, err, session, Counts{})
		return
	}

	toolCount := updateTools(cfg, name, tools)

	prev, _ := states.Get(name)
	prev.Counts.Tools = toolCount
	updateState(name, StateConnected, nil, session, prev.Counts)
}

// registerSessionTools lists the tools a live session exposes and writes them
// into the shared registry, returning the number registered after any
// configured allow/deny filtering. It is the single seam through which a
// (re)connected session's tools enter the registry, so both the initial
// connect and a lazy renew repopulate the tool list the agent sends to the LLM
// instead of leaving it empty.
func registerSessionTools(ctx context.Context, cfg *config.ConfigStore, name string, sess *ClientSession) (int, error) {
	tools, err := getTools(ctx, sess)
	if err != nil {
		return 0, err
	}
	return updateTools(cfg, name, tools), nil
}

func getTools(ctx context.Context, session *ClientSession) ([]*Tool, error) {
	// Always call ListTools to get the actual available tools.
	// The InitializeResult Capabilities.Tools field may be an empty object {},
	// which is valid per MCP spec, but we still need to call ListTools to discover tools.
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func updateTools(cfg *config.ConfigStore, name string, tools []*Tool) int {
	mcpCfg, ok := cfg.Config().MCP[name]
	if ok {
		tools = filterTools(mcpCfg, tools)
	}
	if len(tools) == 0 {
		allTools.Del(name)
		return 0
	}
	var registered []*RegisteredTool
	for _, tool := range tools {
		registered = append(registered, newRegisteredTool(tool))
	}
	allTools.Set(name, registered)
	return len(tools)
}

// filterTools filters tools based on enabled_tools (allow list) and
// disabled_tools (deny list) from the MCP config.
func filterTools(mcpCfg config.MCPConfig, tools []*Tool) []*Tool {
	if len(mcpCfg.EnabledTools) > 0 {
		filtered := make([]*Tool, 0, len(mcpCfg.EnabledTools))
		for _, tool := range tools {
			if slices.Contains(mcpCfg.EnabledTools, tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	if len(mcpCfg.DisabledTools) > 0 {
		filtered := make([]*Tool, 0, len(tools))
		for _, tool := range tools {
			if !slices.Contains(mcpCfg.DisabledTools, tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	return tools
}

// ensureRawBytes normalizes MCP media data into raw binary bytes.
//
// The MCP Go SDK's json.Unmarshal normally base64-decodes
// ImageContent.Data into raw bytes automatically. However, some MCP
// transports (notably Docker over stdio) can deliver data in
// unexpected formats. This function handles both cases:
//
//   - If data looks like a valid base64 string (ASCII-only, decodable)
//     it is decoded and the raw bytes are returned.
//   - If data is already raw binary (contains bytes > 127) it is
//     returned as-is.
func ensureRawBytes(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	normalized := normalizeBase64Input(data)
	if decoded, ok := decodeBase64(normalized); ok {
		return decoded
	}

	// Already raw binary — return unchanged.
	return data
}

func normalizeBase64Input(data []byte) []byte {
	normalized := strings.Join(strings.Fields(string(data)), "")
	return []byte(normalized)
}

func decodeBase64(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return data, true
	}

	for _, b := range data {
		if b > 127 {
			return nil, false
		}
	}

	s := string(data)
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return decoded, true
	}
	decoded, err = base64.RawStdEncoding.DecodeString(s)
	if err == nil {
		return decoded, true
	}
	return nil, false
}

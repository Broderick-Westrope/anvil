package mcp

import (
	"fmt"
	"sync/atomic"
	"unicode"
)

// oauthRenameEnabled controls whether MCP tool names use PascalCase
// for Anthropic OAuth compatibility.
var oauthRenameEnabled atomic.Bool

// SetOAuthRename enables or disables PascalCase tool name transform.
func SetOAuthRename(enabled bool) {
	oauthRenameEnabled.Store(enabled)
}

// OAuthRenameEnabled reports whether PascalCase tool name transform is
// currently enabled.
func OAuthRenameEnabled() bool {
	return oauthRenameEnabled.Load()
}

// PascalCaseToolName capitalizes the first character after the "mcp_"
// prefix in a composite MCP tool name. This transforms names like
// "mcp_docker_find" to "mcp_Docker_find" for Anthropic OAuth billing
// validation compatibility.
func PascalCaseToolName(name string) string {
	const prefix = "mcp_"
	if len(name) <= len(prefix) {
		return name
	}
	if name[:len(prefix)] != prefix {
		return name
	}
	rest := name[len(prefix):]
	if len(rest) == 0 {
		return name
	}
	runes := []rune(rest)
	runes[0] = unicode.ToUpper(runes[0])
	return prefix + string(runes)
}

// OAuthToolName builds a composite MCP tool name with PascalCase
// server name for Anthropic OAuth compatibility.
func OAuthToolName(mcpServerName, toolName string) string {
	capitalized := mcpServerName
	if len(mcpServerName) > 0 {
		runes := []rune(mcpServerName)
		runes[0] = unicode.ToUpper(runes[0])
		capitalized = string(runes)
	}
	return fmt.Sprintf("mcp_%s_%s", capitalized, toolName)
}

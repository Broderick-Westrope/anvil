Find tools on MCP servers whose tools are not in your tool list. These
servers' tools are reached only through search_mcp_tools,
describe_mcp_tool and call_mcp_tool; they are never callable directly by name.

Usage: search with a few keywords, and server if you know it, then
describe_mcp_tool for a tool's input schema, then call_mcp_tool. With
server set, the result starts with the server's instructions; follow them.
Pass server with no query to list every tool name on that server, and do
that whenever a search misses before concluding a tool doesn't exist.

Servers:
{{ range .Servers -}}
- {{ .Name }}: {{ .Description }}
{{ end }}
If a server reports that its authentication has expired, a human must
renew it. Report that to the user and move on; do not retry the call or
try to authenticate on their behalf.

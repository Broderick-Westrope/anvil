Enable a lazy-loaded MCP server's tools for this conversation branch.
Call this when you need capabilities from a server listed below.

Available servers:
{{ range .LazyMCPs -}}
- {{ .Name }}: {{ .Description }}
{{ end -}}

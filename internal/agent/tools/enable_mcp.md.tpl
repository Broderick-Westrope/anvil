Enable a lazy-loaded MCP server's tools for this conversation branch.
Enabling is branch-scoped — it persists for this branch only, not globally.
Call this when you need capabilities from a server listed below.

Available servers:
{{ range .LazyMCPs -}}
- {{ .Name }}: {{ .Description }}
{{ end -}}

If a server reports that its authentication has expired, a human must
renew it. Report that to the user and move on; do not retry the call
or try to authenticate on their behalf.

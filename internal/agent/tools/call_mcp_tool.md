Run a tool on an MCP server listed by search_mcp_tools and return its
result. arguments must match the tool's input schema from
describe_mcp_tool; arguments that don't are rejected, with the schema,
before the server is contacted. The call needs the same permission as
the tool itself.

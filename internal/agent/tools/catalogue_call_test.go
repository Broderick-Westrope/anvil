package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCatalogueCall(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		input   string
		want    CatalogueCall
		wantErr string
	}{
		"object arguments":  {input: `{"server":"s","tool":"t","arguments":{"a":1}}`, want: CatalogueCall{Server: "s", Tool: "t", Arguments: json.RawMessage(`{"a":1}`)}},
		"string arguments":  {input: `{"server":"s","tool":"t","arguments":"{\"a\":1}"}`, want: CatalogueCall{Server: "s", Tool: "t", Arguments: json.RawMessage(`{"a":1}`)}},
		"missing arguments": {input: `{"server":"s","tool":"t"}`, want: CatalogueCall{Server: "s", Tool: "t", Arguments: json.RawMessage(`{}`)}},
		"null arguments":    {input: `{"server":"s","tool":"t","arguments":null}`, want: CatalogueCall{Server: "s", Tool: "t", Arguments: json.RawMessage(`{}`)}},
		"not json":          {input: `{`, wantErr: "invalid input: unexpected end of JSON input"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCatalogueCall(tc.input)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCatalogueCallInputRoundTrips(t *testing.T) {
	t.Parallel()
	call := CatalogueCall{Server: "s", Tool: "t", Arguments: json.RawMessage(`{"a":1}`)}

	require.Equal(t, `{"server":"s","tool":"t","arguments":{"a":1}}`, call.Input())
	parsed, err := ParseCatalogueCall(call.Input())
	require.NoError(t, err)
	require.Equal(t, call, parsed)
}

func TestValidateArgumentsReportsInvalidJSON(t *testing.T) {
	t.Parallel()
	tool := bareTool("s", "t", "")

	require.Equal(t, "arguments are not valid JSON: unexpected end of JSON input", validateArguments(tool.MCP, []byte("{")))
}

func TestCallPassesThroughSchemasOfDraftsItCantCheck(t *testing.T) {
	drafts := map[string]string{
		"draft-04": "http://json-schema.org/draft-04/schema#",
		"2019-09":  "https://json-schema.org/draft/2019-09/schema",
	}
	for name, draft := range drafts {
		t.Run(name, func(t *testing.T) {
			server := "cat-draft-" + name
			schema := map[string]any{"$schema": draft, "type": "object", "required": []any{"query"}}
			f := newCatalogueFixture(t, server, "", false, catalogueServerTool{name: "q", description: "Q.", schema: schema})

			resp := f.run(t, CallMCPToolToolName, map[string]any{"server": server, "tool": "q", "arguments": map[string]any{}})

			require.False(t, resp.IsError, resp.Content)
			require.Equal(t, int32(1), f.calls.Load(), "the server checks what Anvil can't")
		})
	}
}

func TestCallStillValidatesDraft07Schemas(t *testing.T) {
	schema := map[string]any{"$schema": "http://json-schema.org/draft-07/schema#", "type": "object", "required": []any{"query"}}
	f := newCatalogueFixture(t, "cat-draft-07", "", false, catalogueServerTool{name: "q", description: "Q.", schema: schema})

	resp := f.run(t, CallMCPToolToolName, map[string]any{"server": "cat-draft-07", "tool": "q", "arguments": map[string]any{}})

	require.True(t, strings.HasPrefix(resp.Content, InvalidArgumentsPrefix), resp.Content)
	require.Zero(t, f.calls.Load())
}

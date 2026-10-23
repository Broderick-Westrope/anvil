package tools

import (
	"encoding/json"
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

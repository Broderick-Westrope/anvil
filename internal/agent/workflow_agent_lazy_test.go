package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLazyServersForToolNames(t *testing.T) {
	t.Parallel()

	lazy := map[string]string{"datadog": "obs", "linear": "issues", "my_srv": "x"}
	tests := map[string]struct {
		names []string
		want  []string
	}{
		"plain name":           {names: []string{"mcp_datadog_query_logs"}, want: []string{"datadog"}},
		"oauth-renamed prefix": {names: []string{"mcp_Linear_list_issues"}, want: []string{"linear"}},
		"underscore in server": {names: []string{"mcp_my_srv_do"}, want: []string{"my_srv"}},
		"built-in name":        {names: []string{"view"}, want: nil},
		"unknown server":       {names: []string{"mcp_ghost_x"}, want: nil},
		"deduplicated":         {names: []string{"mcp_datadog_a", "mcp_datadog_b"}, want: []string{"datadog"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, lazyServersFor(tc.names, lazy))
		})
	}
}

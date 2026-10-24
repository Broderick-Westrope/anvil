package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPRouteInfoStrings(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		route     MCPRouteInfo
		want      string
		wantShort string
	}{
		"catalogue": {
			route:     MCPRouteInfo{Server: "s", Catalogue: true, Reason: "auto: lazy", Short: "lazy"},
			want:      "catalogue (auto: lazy)",
			wantShort: "catalogue (lazy)",
		},
		"direct": {
			route:     MCPRouteInfo{Server: "s", Reason: "route: direct", Short: "configured"},
			want:      "direct (route: direct)",
			wantShort: "direct (configured)",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.route.String())
			require.Equal(t, tc.wantShort, tc.route.ShortString())
		})
	}
}

package app

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/stretchr/testify/require"
)

type yoloPermissions struct {
	permission.Service
	level config.YoloLevel
}

func (p yoloPermissions) YoloLevel() config.YoloLevel { return p.level }

func TestSkipWorkflowApprovalOnlyAtFullYolo(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		level config.YoloLevel
		want  bool
	}{
		"off":      {level: config.YoloOff},
		"standard": {level: config.YoloStandard},
		"full":     {level: config.YoloFull, want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := &App{Permissions: yoloPermissions{level: tc.level}}
			require.Equal(t, tc.want, app.skipWorkflowApproval())
		})
	}
}

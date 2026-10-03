package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/stretchr/testify/require"
)

func TestModelInfo_BouncerIndicator(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()}

	t.Run("shown when shadow", func(t *testing.T) {
		t.Parallel()
		u := newTestUI()
		u.com.Workspace = &testWorkspace{cfg: cfg, bouncerConfigured: true, bouncerMode: permission.BouncerShadow}
		require.Contains(t, u.modelInfo(40), "bouncer:shadow")
	})

	t.Run("hidden when off", func(t *testing.T) {
		t.Parallel()
		u := newTestUI()
		u.com.Workspace = &testWorkspace{cfg: cfg, bouncerConfigured: false, bouncerMode: permission.BouncerOff}
		require.NotContains(t, u.modelInfo(40), "bouncer:")
	})
}

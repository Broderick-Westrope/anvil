package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/stretchr/testify/require"
)

func TestRenderHeaderDetails_BouncerIndicator(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()}

	t.Run("shown when enforce", func(t *testing.T) {
		t.Parallel()
		com := common.DefaultCommon(&testWorkspace{cfg: cfg, bouncerConfigured: true, bouncerMode: permission.BouncerEnforce})
		got := renderHeaderDetails(com, nil, 0, false, 200)
		require.Contains(t, got, "bouncer:enforce")
	})

	t.Run("hidden when off", func(t *testing.T) {
		t.Parallel()
		com := common.DefaultCommon(&testWorkspace{cfg: cfg, bouncerConfigured: false, bouncerMode: permission.BouncerOff})
		got := renderHeaderDetails(com, nil, 0, false, 200)
		require.NotContains(t, got, "bouncer:")
	})
}

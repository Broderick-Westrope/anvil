package dialog

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

// bouncerCommandsWorkspace is a minimal [workspace.Workspace] stub for
// exercising the "cycle_bouncer" command palette item.
type bouncerCommandsWorkspace struct {
	workspace.Workspace
	cfg        *config.Config
	configured bool
	mode       permission.BouncerMode
}

func (w *bouncerCommandsWorkspace) Config() *config.Config { return w.cfg }

func (w *bouncerCommandsWorkspace) PermissionBouncerConfigured() bool { return w.configured }

func (w *bouncerCommandsWorkspace) PermissionBouncerMode() permission.BouncerMode {
	return w.mode
}

func testCommandsConfig() *config.Config {
	return &config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()}
}

func findCommandItem(items []*CommandItem, id string) *CommandItem {
	for _, item := range items {
		if item.ID() == id {
			return item
		}
	}
	return nil
}

func TestDefaultCommands_CycleBouncerAbsentWhenNotConfigured(t *testing.T) {
	t.Parallel()

	s := styles.TokyoNight()
	com := &common.Common{
		Styles:    &s,
		Workspace: &bouncerCommandsWorkspace{cfg: testCommandsConfig(), configured: false},
	}
	c := &Commands{com: com}

	require.Nil(t, findCommandItem(c.defaultCommands(), "cycle_bouncer"))
}

func TestDefaultCommands_CycleBouncerPresentWhenConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mode          permission.BouncerMode
		wantLabelHas  []string
		wantLabelMiss []string
	}{
		{
			name:         "off cycles to shadow",
			mode:         permission.BouncerOff,
			wantLabelHas: []string{"Off", "Shadow"},
		},
		{
			name:         "shadow cycles to enforce",
			mode:         permission.BouncerShadow,
			wantLabelHas: []string{"Shadow", "Enforce"},
		},
		{
			name:         "enforce cycles to off",
			mode:         permission.BouncerEnforce,
			wantLabelHas: []string{"Enforce", "Off"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := styles.TokyoNight()
			com := &common.Common{
				Styles:    &s,
				Workspace: &bouncerCommandsWorkspace{cfg: testCommandsConfig(), configured: true, mode: tc.mode},
			}
			c := &Commands{com: com}

			item := findCommandItem(c.defaultCommands(), "cycle_bouncer")
			require.NotNil(t, item)
			for _, want := range tc.wantLabelHas {
				require.Contains(t, item.title, want)
			}
			require.Equal(t, ActionCycleBouncerMode{}, item.Action())
		})
	}
}

func TestBouncerModeLabel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Off", bouncerModeLabel(permission.BouncerOff))
	require.Equal(t, "Shadow", bouncerModeLabel(permission.BouncerShadow))
	require.Equal(t, "Enforce", bouncerModeLabel(permission.BouncerEnforce))
}

func TestNextBouncerMode(t *testing.T) {
	t.Parallel()

	require.Equal(t, permission.BouncerShadow, nextBouncerMode(permission.BouncerOff))
	require.Equal(t, permission.BouncerEnforce, nextBouncerMode(permission.BouncerShadow))
	require.Equal(t, permission.BouncerOff, nextBouncerMode(permission.BouncerEnforce))
}

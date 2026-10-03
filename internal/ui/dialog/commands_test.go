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

// assessorCommandsWorkspace is a minimal [workspace.Workspace] stub for
// exercising the "cycle_assessor" command palette item.
type assessorCommandsWorkspace struct {
	workspace.Workspace
	cfg        *config.Config
	configured bool
	mode       permission.AssessorMode
}

func (w *assessorCommandsWorkspace) Config() *config.Config { return w.cfg }

func (w *assessorCommandsWorkspace) PermissionAssessorConfigured() bool { return w.configured }

func (w *assessorCommandsWorkspace) PermissionAssessorMode() permission.AssessorMode {
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

func TestDefaultCommands_CycleAssessorAbsentWhenNotConfigured(t *testing.T) {
	t.Parallel()

	s := styles.TokyoNight()
	com := &common.Common{
		Styles:    &s,
		Workspace: &assessorCommandsWorkspace{cfg: testCommandsConfig(), configured: false},
	}
	c := &Commands{com: com}

	require.Nil(t, findCommandItem(c.defaultCommands(), "cycle_assessor"))
}

func TestDefaultCommands_CycleAssessorPresentWhenConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mode          permission.AssessorMode
		wantLabelHas  []string
		wantLabelMiss []string
	}{
		{
			name:         "off cycles to shadow",
			mode:         permission.AssessorOff,
			wantLabelHas: []string{"Off", "Shadow"},
		},
		{
			name:         "shadow cycles to enforce",
			mode:         permission.AssessorShadow,
			wantLabelHas: []string{"Shadow", "Enforce"},
		},
		{
			name:         "enforce cycles to off",
			mode:         permission.AssessorEnforce,
			wantLabelHas: []string{"Enforce", "Off"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := styles.TokyoNight()
			com := &common.Common{
				Styles:    &s,
				Workspace: &assessorCommandsWorkspace{cfg: testCommandsConfig(), configured: true, mode: tc.mode},
			}
			c := &Commands{com: com}

			item := findCommandItem(c.defaultCommands(), "cycle_assessor")
			require.NotNil(t, item)
			for _, want := range tc.wantLabelHas {
				require.Contains(t, item.title, want)
			}
			require.Equal(t, ActionCycleAssessorMode{}, item.Action())
		})
	}
}

func TestAssessorModeLabel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Off", assessorModeLabel(permission.AssessorOff))
	require.Equal(t, "Shadow", assessorModeLabel(permission.AssessorShadow))
	require.Equal(t, "Enforce", assessorModeLabel(permission.AssessorEnforce))
}

func TestNextAssessorMode(t *testing.T) {
	t.Parallel()

	require.Equal(t, permission.AssessorShadow, nextAssessorMode(permission.AssessorOff))
	require.Equal(t, permission.AssessorEnforce, nextAssessorMode(permission.AssessorShadow))
	require.Equal(t, permission.AssessorOff, nextAssessorMode(permission.AssessorEnforce))
}

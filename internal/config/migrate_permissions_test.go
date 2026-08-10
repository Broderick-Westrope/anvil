package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrateAllowedTools_Empty(t *testing.T) {
	t.Parallel()

	got := MigrateAllowedTools(nil)
	require.Empty(t, got)

	got = MigrateAllowedTools([]string{})
	require.Empty(t, got)
}

func TestMigrateAllowedTools_SimpleTool(t *testing.T) {
	t.Parallel()

	got := MigrateAllowedTools([]string{"bash"})
	require.Equal(t, []PermissionRule{
		{ToolPattern: "bash", Action: PermissionAllow},
	}, got)
}

func TestMigrateAllowedTools_MixedEntries(t *testing.T) {
	t.Parallel()

	got := MigrateAllowedTools([]string{"bash:execute", "edit"})
	require.Equal(t, []PermissionRule{
		{ToolPattern: "bash", Action: PermissionAllow},
		{ToolPattern: "edit", Action: PermissionAllow},
	}, got)
}

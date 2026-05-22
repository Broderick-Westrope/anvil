package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

func TestCustomCommandRawArgumentsPlaceholderOpensArgumentDialog(t *testing.T) {
	t.Parallel()

	action := dialog.ActionRunCustomCommand{Content: "Review $ARGUMENTS"}
	require.True(t, customCommandNeedsArgumentDialog(action))

	args := customCommandDialogArguments(action)
	require.Len(t, args, 1)
	require.Equal(t, "ARGUMENTS", args[0].ID)
	require.Equal(t, "Arguments", args[0].Title)
}

func TestSubstituteCustomCommandArgsUsesRawArgumentsPlaceholder(t *testing.T) {
	t.Parallel()

	action := dialog.ActionRunCustomCommand{
		Content: "Review $ARGUMENTS for $AREA",
		Arguments: []commands.Argument{
			{ID: "AREA", Title: "Area"},
		},
		Args: map[string]string{
			"ARGUMENTS": "the diff",
			"AREA":      "security",
		},
	}

	require.Equal(t, "Review the diff for security", substituteCustomCommandArgs(action))
}

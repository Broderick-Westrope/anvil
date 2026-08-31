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

func TestCustomCommandLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action dialog.ActionRunCustomCommand
		want   string
	}{
		{
			name:   "no args",
			action: dialog.ActionRunCustomCommand{Name: "review"},
			want:   "/review",
		},
		{
			name: "raw arguments",
			action: dialog.ActionRunCustomCommand{
				Name: "review",
				Args: map[string]string{"ARGUMENTS": "fix typo"},
			},
			want: "/review fix typo",
		},
		{
			name: "named args in declared order",
			action: dialog.ActionRunCustomCommand{
				Name: "review",
				Arguments: []commands.Argument{
					{ID: "AREA"},
					{ID: "DEPTH"},
				},
				Args: map[string]string{
					"DEPTH": "deep",
					"AREA":  "security",
				},
			},
			want: `/review AREA="security" DEPTH="deep"`,
		},
		{
			name: "empty values omitted",
			action: dialog.ActionRunCustomCommand{
				Name: "review",
				Arguments: []commands.Argument{
					{ID: "AREA"},
				},
				Args: map[string]string{"AREA": "  "},
			},
			want: "/review",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, customCommandLine(tt.action))
		})
	}
}

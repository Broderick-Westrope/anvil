package model

import (
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
)

const rawArgumentsID = "ARGUMENTS"

func customCommandNeedsArgumentDialog(action dialog.ActionRunCustomCommand) bool {
	return action.Args == nil && (len(action.Arguments) > 0 || strings.Contains(action.Content, "$ARGUMENTS"))
}

func customCommandDialogArguments(action dialog.ActionRunCustomCommand) []commands.Argument {
	args := make([]commands.Argument, 0, len(action.Arguments)+1)
	if strings.Contains(action.Content, "$ARGUMENTS") && !hasArgument(action.Arguments, rawArgumentsID) {
		args = append(args, commands.Argument{
			ID:          rawArgumentsID,
			Title:       "Arguments",
			Description: "Raw arguments to substitute for $ARGUMENTS.",
		})
	}
	args = append(args, action.Arguments...)
	return args
}

func substituteCustomCommandArgs(action dialog.ActionRunCustomCommand) string {
	if action.Args == nil {
		return action.Content
	}

	rawArguments := action.Args[rawArgumentsID]
	namedArgs := make(map[string]string, len(action.Args))
	for name, value := range action.Args {
		if name == rawArgumentsID {
			continue
		}
		namedArgs[name] = value
	}
	return commands.SubstituteArgs(action.Content, namedArgs, rawArguments)
}

func hasArgument(args []commands.Argument, id string) bool {
	for _, arg := range args {
		if arg.ID == id {
			return true
		}
	}
	return false
}

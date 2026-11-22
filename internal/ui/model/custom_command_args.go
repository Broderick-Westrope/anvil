package model

import (
	"fmt"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
)

const rawArgumentsID = "ARGUMENTS"

const customCommandArgsTitle = "Custom Command Arguments"

// openCustomCommandArgs opens the arguments dialog for a typed command in
// front of whatever is already open, with rawArgs as the starting value
// for $ARGUMENTS.
func (m *UI) openCustomCommandArgs(action dialog.ActionRunCustomCommand, rawArgs string) {
	args := customCommandDialogArguments(action)
	for i := range args {
		if args[i].ID == rawArgumentsID {
			args[i].DefaultValue = rawArgs
		}
	}
	m.dialog.OpenDialog(dialog.NewArguments(m.com, customCommandArgsTitle, customCommandArgsDescription(action), args, action))
}

// customCommandArgsDescription says where the dialog's fields come from,
// since a $NAME placeholder is easy to write by accident, for instance when
// a command mentions an environment variable.
func customCommandArgsDescription(action dialog.ActionRunCustomCommand) string {
	if action.Location == "" {
		return "Each field fills a $NAME placeholder in the command"
	}
	return "Each field fills a $NAME placeholder in " + action.Location
}

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

// customCommandLine reconstructs the command line the user invoked,
// e.g. "/review fix typo" or `/review scope="auth"`. Used as the compact
// display form of an expanded command message.
func customCommandLine(action dialog.ActionRunCustomCommand) string {
	line := "/" + action.Name
	if action.Args == nil {
		return line
	}
	if raw := strings.TrimSpace(action.Args[rawArgumentsID]); raw != "" {
		line += " " + raw
	}
	for _, arg := range action.Arguments {
		if arg.ID == rawArgumentsID {
			continue
		}
		if v := strings.TrimSpace(action.Args[arg.ID]); v != "" {
			line += fmt.Sprintf(" %s=%q", arg.ID, v)
		}
	}
	return line
}

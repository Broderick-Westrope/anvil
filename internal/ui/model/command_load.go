package model

import (
	"fmt"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// loadCommands loads the custom commands with what stops each one running
// added to its problems, so a command that can't run is shown as broken.
// It does IO, so call it from a command.
func loadCommands(com *common.Common) []commands.CustomCommand {
	cmds, err := commands.LoadAllCommands(com.Config(), nil)
	if err != nil {
		slog.Error("Failed to load custom commands", "error", err)
	}
	return com.Workspace.AnnotateCommands(cmds)
}

// commandsLoaded delivers freshly loaded commands, with a warning beside
// them when some won't run, so the userCommandsLoadedMsg case in update
// needs no warning of its own.
func commandsLoaded(cmds []commands.CustomCommand) tea.Msg {
	loaded := userCommandsLoadedMsg{Commands: cmds}
	warning := brokenWorkflowsWarning(cmds)
	if warning == "" {
		return loaded
	}
	return tea.Batch(func() tea.Msg { return loaded }, util.ReportWarn(warning))()
}

// brokenWorkflowsWarning describes the workflow commands that won't run, or
// returns "" when there are none.
func brokenWorkflowsWarning(cmds []commands.CustomCommand) string {
	var problems []string
	for _, cmd := range cmds {
		if cmd.Workflow == "" || len(cmd.Problems) == 0 {
			continue
		}
		problems = append(problems, fmt.Sprintf("/%s: %s", cmd.SlashName(), strings.Join(cmd.Problems, "; ")))
	}
	if len(problems) == 0 {
		return ""
	}
	return "Broken workflow commands: " + strings.Join(problems, " · ")
}

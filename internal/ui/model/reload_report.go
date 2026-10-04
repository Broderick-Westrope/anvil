package model

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
)

// reloadErrMaxRunes bounds an error in the status line; the full text is
// logged.
const reloadErrMaxRunes = 120

// configReloadedMsg is sent when "Reload Config & Plugins" finishes.
// SkillStates and CustomCommands are set only when plugins reloaded.
type configReloadedMsg struct {
	Report         workspace.ReloadReport
	SkillStates    []*skills.SkillState
	CustomCommands []commands.CustomCommand
}

// reloadConfig shows a status straight away, since provider discovery can
// take seconds, then reloads config and plugins asynchronously.
func (m *UI) reloadConfig() tea.Cmd {
	return tea.Sequence(util.ReportInfo("Reloading config…"), m.runConfigReload)
}

func (m *UI) runConfigReload() tea.Msg {
	r := m.com.Workspace.ReloadConfigAndPlugins(context.Background())
	msg := configReloadedMsg{Report: r}
	if !pluginsReloaded(r) {
		return msg
	}
	// Custom commands include plugin commands.
	customCmds, err := commands.LoadAllCommands(m.com.Config(), nil)
	if err != nil {
		slog.Error("Failed to reload custom commands after plugin reload", "error", err)
	}
	msg.SkillStates = m.com.Workspace.SkillStates()
	msg.CustomCommands = customCmds
	return msg
}

func (m *UI) handleConfigReloaded(msg configReloadedMsg) tea.Cmd {
	r := msg.Report
	logReloadReport(r)
	if pluginsReloaded(r) {
		m.skillStates = msg.SkillStates
		m.customCommands = msg.CustomCommands
		m.slashAC.SetItems(m.buildSlashACItems())
		if dia := m.dialog.Dialog(dialog.CommandsID); dia != nil {
			if cmdsDialog, ok := dia.(*dialog.Commands); ok {
				cmdsDialog.SetCustomCommands(m.customCommands)
			}
		}
	}
	text, isErr := formatReloadReport(r)
	typ := util.InfoTypeInfo
	if isErr {
		typ = util.InfoTypeError
	}
	return util.CmdHandler(util.InfoMsg{Type: typ, Msg: text})
}

func pluginsReloaded(r workspace.ReloadReport) bool {
	return r.PluginsErr == nil && !r.PluginsSkipped && !r.PluginsBusy
}

func logReloadReport(r workspace.ReloadReport) {
	if r.ConfigErr != nil {
		slog.Error("Config reload failed", "error", r.ConfigErr)
	}
	if r.ApplyErr != nil {
		slog.Error("Reloaded config not applied", "error", r.ApplyErr)
	}
	if r.PluginsErr != nil {
		slog.Error("Plugin reload failed", "error", r.PluginsErr)
	}
	for _, w := range r.PluginWarnings {
		slog.Warn("Plugin content skipped on reload", "path", w.Path, "error", w.Err)
	}
}

// formatReloadReport renders a reload report as one status line. The bool
// reports whether the reload failed in part, including plugins skipped
// because the agent was busy; plugin warnings and pending restarts don't
// count.
func formatReloadReport(r workspace.ReloadReport) (string, bool) {
	configOK := r.ConfigErr == nil && r.ApplyErr == nil
	pluginsOK := pluginsReloaded(r)

	var parts []string
	switch {
	case configOK && pluginsOK:
		parts = append(parts, "Config and plugins reloaded")
	default:
		switch {
		case r.ConfigErr != nil:
			parts = append(parts, "Config not reloaded: "+truncateReloadErr(r.ConfigErr))
		case r.ApplyErr != nil:
			parts = append(parts, "Config reloaded but not applied: "+truncateReloadErr(r.ApplyErr))
		default:
			parts = append(parts, "Config reloaded")
		}
		switch {
		case r.PluginsSkipped:
			parts = append(parts, "plugins load once a model is set up")
		case r.PluginsBusy:
			parts = append(parts, "plugins not reloaded: agent is busy")
		case r.PluginsErr != nil:
			parts = append(parts, "plugins failed: "+truncateReloadErr(r.PluginsErr))
		default:
			parts = append(parts, "plugins reloaded")
		}
	}

	if pluginsOK && len(r.PluginWarnings) > 0 {
		noun := "warnings"
		if len(r.PluginWarnings) == 1 {
			noun = "warning"
		}
		parts = append(parts, fmt.Sprintf("%d plugin %s: %s", len(r.PluginWarnings), noun, r.PluginWarnings[0].Path))
	}

	restart := slices.DeleteFunc(slices.Clone(r.RestartRequired), func(l string) bool { return l == "bouncer.mode" })
	if len(restart) > 0 {
		verb := "need"
		if len(restart) == 1 {
			verb = "needs"
		}
		parts = append(parts, fmt.Sprintf("%s %s /reload-instance", strings.Join(restart, ", "), verb))
	}
	if slices.Contains(r.RestartRequired, "bouncer.mode") {
		parts = append(parts, "bouncer mode differs from config (ctrl+q)")
	}

	isErr := r.ConfigErr != nil || r.ApplyErr != nil || r.PluginsErr != nil || r.PluginsBusy
	return strings.Join(parts, " · "), isErr
}

func truncateReloadErr(err error) string {
	s := err.Error()
	runes := []rune(s)
	if len(runes) <= reloadErrMaxRunes {
		return s
	}
	return string(runes[:reloadErrMaxRunes-1]) + "…"
}

package model

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
)

// promptWidth is the editor gutter width: a three-cell badge plus a
// one-cell margin.
const promptWidth = 4

// promptBadge is a mode indicator drawn on one line of the prompt gutter.
type promptBadge struct {
	focused lipgloss.Style
	blurred lipgloss.Style
}

// promptModes is the permission state the editor prompt reflects. It is
// captured when the state changes so rendering never queries the
// workspace.
type promptModes struct {
	assessor permission.AssessorMode
	yolo     config.YoloLevel
}

func (pm promptModes) assessorOn() bool {
	return pm.assessor == permission.AssessorShadow || pm.assessor == permission.AssessorEnforce
}

// active reports whether any mode badge is shown.
func (pm promptModes) active() bool {
	return pm.assessorOn() || pm.yolo != config.YoloOff
}

// currentPromptModes reads the permission state from the workspace.
func (m *UI) currentPromptModes() promptModes {
	pm := promptModes{yolo: m.com.Workspace.PermissionYoloLevel()}
	if m.com.Workspace.PermissionAssessorConfigured() {
		pm.assessor = m.com.Workspace.PermissionAssessorMode()
	}
	return pm
}

// refreshEditorPrompt recomputes the prompt badges from the current
// permission state. Call it whenever yolo or the assessor mode changes.
func (m *UI) refreshEditorPrompt() {
	m.promptModes = m.currentPromptModes()
	m.promptBadges = m.badgesFor(m.promptModes)
	if m.promptModes.active() {
		m.textarea.SetPromptFunc(promptWidth, m.modePromptFunc)
		return
	}
	m.textarea.SetPromptFunc(promptWidth, m.normalPromptFunc)
}

// badgesFor returns the badges to draw, top to bottom: the assessor
// first, then yolo.
func (m *UI) badgesFor(pm promptModes) []promptBadge {
	t := m.com.Styles.Editor
	var badges []promptBadge
	switch pm.assessor {
	case permission.AssessorShadow:
		badges = append(badges, promptBadge{t.PromptShadowFocused, t.PromptShadowBlurred})
	case permission.AssessorEnforce:
		if pm.yolo == config.YoloFull {
			// Full yolo returns before the assessor runs, so a bright
			// badge would claim protection that isn't there.
			badges = append(badges, promptBadge{t.PromptEnforceInactive, t.PromptEnforceBlurred})
		} else {
			badges = append(badges, promptBadge{t.PromptEnforceFocused, t.PromptEnforceBlurred})
		}
	}
	switch pm.yolo {
	case config.YoloStandard:
		badges = append(badges, promptBadge{t.PromptYoloFocused, t.PromptYoloBlurred})
	case config.YoloFull:
		badges = append(badges, promptBadge{t.PromptFullYoloFocused, t.PromptFullYoloBlurred})
	}
	return badges
}

// modePromptFunc draws a badge on each of the first lines and dots below,
// tinted by the riskiest active mode.
func (m *UI) modePromptFunc(info textarea.PromptInfo) string {
	if info.LineNumber < len(m.promptBadges) {
		b := m.promptBadges[info.LineNumber]
		if info.Focused {
			return b.focused.Render()
		}
		return b.blurred.Render()
	}
	t := m.com.Styles.Editor
	if !info.Focused {
		return t.PromptModeDotsBlurred.Render()
	}
	switch m.promptModes.yolo {
	case config.YoloFull:
		return t.PromptFullYoloDotsFocused.Render()
	case config.YoloStandard:
		return t.PromptYoloDotsFocused.Render()
	default:
		return t.PromptAssessorDotsFocused.Render()
	}
}

// normalPromptFunc returns the normal editor prompt style ("  > " on first
// line, "::: " on subsequent lines).
func (m *UI) normalPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return "  > "
		}
		return "::: "
	}
	if info.Focused {
		return t.Editor.PromptNormalFocused.Render()
	}
	return t.Editor.PromptNormalBlurred.Render()
}

// modePlaceholder returns the editor placeholder for the current yolo
// level, or "" to keep the normal placeholder.
func (pm promptModes) modePlaceholder() string {
	switch pm.yolo {
	case config.YoloFull:
		return "Full yolo: no permission checks"
	case config.YoloStandard:
		return "Yolo mode!"
	default:
		return ""
	}
}

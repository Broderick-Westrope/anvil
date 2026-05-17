package model

import (
	"cmp"
	"fmt"
	"image"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/logo"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/layout"
)

// modelInfo renders the current model information including reasoning
// settings and context usage/cost for the sidebar. When drilled in to a
// subagent session, stats and elapsed time for that session are appended.
func (m *UI) modelInfo(width int) string {
	model := m.selectedLargeModel()
	reasoningInfo := ""
	providerName := ""

	if model != nil {
		// Get provider name first.
		providerConfig, ok := m.com.Config().Providers.Get(model.ModelCfg.Provider)
		if ok {
			providerName = providerConfig.Name

			// Only check reasoning if model can reason.
			if model.CatwalkCfg.CanReason {
				if len(model.CatwalkCfg.ReasoningLevels) == 0 {
					if model.ModelCfg.Think {
						reasoningInfo = "Thinking On"
					} else {
						reasoningInfo = "Thinking Off"
					}
				} else {
					reasoningEffort := cmp.Or(model.ModelCfg.ReasoningEffort, model.CatwalkCfg.DefaultReasoningEffort)
					reasoningInfo = fmt.Sprintf("Reasoning %s", common.FormatReasoningEffort(reasoningEffort))
				}
			}
		}
	}

	// Use the drilled-in session's stats when applicable; fall back to root.
	activeSession := m.session
	if m.isDrilledIn() {
		top := m.drillStack[len(m.drillStack)-1]
		if top.session != nil {
			activeSession = top.session
		}
	}

	var modelContext *common.ModelContextInfo
	if model != nil && activeSession != nil {
		modelContext = &common.ModelContextInfo{
			ContextUsed:  activeSession.CompletionTokens + activeSession.PromptTokens,
			Cost:         activeSession.Cost,
			ModelContext: model.CatwalkCfg.ContextWindow,
		}
	}
	var modelName string
	if model != nil {
		modelName = model.CatwalkCfg.Name
	}

	// Build extra sidebar lines for drilled-in subagent sessions.
	var extraLines []string
	if m.isDrilledIn() {
		t := m.com.Styles
		turns, toolCalls := m.viewedSessionStats()
		statsStr := t.ModelInfo.Stats.Render(fmt.Sprintf("%d turns · %d tools", turns, toolCalls))
		extraLines = append(extraLines, statsStr)

		// Compute elapsed time using the cached session timestamps.
		if activeSession != nil {
			var dur time.Duration
			if m.isViewedSubagentRunning() {
				dur = time.Since(time.Unix(activeSession.CreatedAt, 0))
			} else if activeSession.UpdatedAt > activeSession.CreatedAt {
				dur = time.Unix(activeSession.UpdatedAt, 0).Sub(time.Unix(activeSession.CreatedAt, 0))
			}
			if dur > 0 {
				elapsedStr := t.ModelInfo.Elapsed.Render(common.FormatDuration(dur))
				extraLines = append(extraLines, elapsedStr)
			}
		}
	}

	return common.ModelInfo(m.com.Styles, modelName, providerName, reasoningInfo, modelContext, width, m.hyperCredits, extraLines...)
}

// viewedSessionStats returns the turn and tool-call counts for the session
// currently shown in the chat pane.
//
// When drilled in, it looks up the parent agent item (the same way
// isViewedSubagentRunning does) and delegates to its cached Stats(). This
// avoids an O(n) scan on every sidebar render.
//
// When viewing the root session, the O(n) scan is kept because there is no
// parent agent item with pre-computed stats. The root session is only shown
// when NOT drilled in, so tick-driven performance is not a concern there.
func (m *UI) viewedSessionStats() (turns, toolCalls int) {
	if m.isDrilledIn() {
		sid := m.viewedSessionID()
		_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(sid)
		if !ok {
			return 0, 0
		}

		item := m.findParentMessageItem(toolCallID)
		if da, ok := item.(chat.DrillableAgent); ok {
			return da.Stats()
		}
		return 0, 0
	}

	// Root session: derive counts from the chat item list.
	c := m.activeChat()
	for i := range c.Len() {
		item := c.ItemAt(i)
		if _, ok := item.(*chat.AssistantMessageItem); ok {
			turns++
		}
		if _, ok := item.(chat.ToolMessageItem); ok {
			toolCalls++
		}
	}
	return
}

// isViewedSubagentRunning reports whether the subagent session currently
// being viewed is still running. It determines running state from the
// parent agent item's ToolStatus rather than a time heuristic.
func (m *UI) isViewedSubagentRunning() bool {
	if !m.isDrilledIn() {
		return false
	}
	sid := m.viewedSessionID()
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(sid)
	if !ok {
		return false
	}

	item := m.findParentMessageItem(toolCallID)
	tmi, ok := item.(chat.ToolMessageItem)
	if !ok {
		return false
	}
	return tmi.Status() == chat.ToolStatusRunning && !tmi.ToolCall().Finished
}

// getDynamicHeightLimits will give us the num of items to show in each section based on the height
// some items are more important than others.
func getDynamicHeightLimits(availableHeight, fileCount, lspCount, mcpCount, skillCount int) (maxFiles, maxLSPs, maxMCPs, maxSkills int) {
	const (
		minItemsPerSection = 2
		// Keep these high so dynamic layout uses available sidebar space
		// instead of hitting small hard limits.
		defaultMaxFilesShown    = 1000
		defaultMaxLSPsShown     = 1000
		defaultMaxMCPsShown     = 1000
		defaultMaxSkillsShown   = 1000
		minAvailableHeightLimit = 10
	)

	if availableHeight < minAvailableHeightLimit {
		return minItemsPerSection, minItemsPerSection, minItemsPerSection, minItemsPerSection
	}

	maxFiles = minItemsPerSection
	maxLSPs = minItemsPerSection
	maxMCPs = minItemsPerSection
	maxSkills = minItemsPerSection

	remainingHeight := max(0, availableHeight-(minItemsPerSection*4))

	sectionValues := []*int{&maxFiles, &maxLSPs, &maxMCPs, &maxSkills}
	sectionCaps := []int{defaultMaxFilesShown, defaultMaxLSPsShown, defaultMaxMCPsShown, defaultMaxSkillsShown}
	sectionNeeds := []int{max(0, fileCount-maxFiles), max(0, lspCount-maxLSPs), max(0, mcpCount-maxMCPs), max(0, skillCount-maxSkills)}

	for remainingHeight > 0 {
		allocated := false
		for i, section := range sectionValues {
			if remainingHeight == 0 {
				break
			}
			if sectionNeeds[i] == 0 || *section >= sectionCaps[i] {
				continue
			}
			*section = *section + 1
			sectionNeeds[i]--
			remainingHeight--
			allocated = true
		}
		if !allocated {
			break
		}
	}

	for remainingHeight > 0 {
		allocated := false
		for i, section := range sectionValues {
			if remainingHeight == 0 {
				break
			}
			if *section >= sectionCaps[i] {
				continue
			}
			*section = *section + 1
			remainingHeight--
			allocated = true
		}
		if !allocated {
			break
		}
	}

	return maxFiles, maxLSPs, maxMCPs, maxSkills
}

// sidebar renders the chat sidebar containing session title, working
// directory, model info, file list, LSP status, and MCP status.
func (m *UI) drawSidebar(scr uv.Screen, area uv.Rectangle) {
	if m.session == nil {
		return
	}

	const logoHeightBreakpoint = 30

	t := m.com.Styles
	width := area.Dx()
	height := area.Dy()

	title := t.Sidebar.SessionTitle.Width(width).MaxHeight(2).Render(m.session.Title)
	cwd := common.PrettyPath(t, m.com.Workspace.WorkingDir(), width)
	sidebarLogo := m.sidebarLogo
	if height < logoHeightBreakpoint {
		sidebarLogo = logo.SmallRender(m.com.Styles, width, logo.Opts{
			RandomColor: true,
		})
	}
	blocks := []string{
		sidebarLogo,
		title,
		"",
		cwd,
		"",
		m.modelInfo(width),
		"",
	}

	sidebarHeader := lipgloss.JoinVertical(
		lipgloss.Left,
		blocks...,
	)

	var remainingHeightArea image.Rectangle
	layout.Vertical(
		layout.Len(lipgloss.Height(sidebarHeader)),
		layout.Fill(1),
	).Split(m.layout.sidebar).Assign(new(image.Rectangle), &remainingHeightArea)
	remainingHeight := remainingHeightArea.Dy() - 6
	filesCount := 0
	for _, f := range m.sessionFiles {
		if f.Additions == 0 && f.Deletions == 0 {
			continue
		}
		filesCount++
	}

	lspsCount := len(m.lspStates)

	mcpsCount := 0
	for _, mcpCfg := range m.com.Config().MCP.Sorted() {
		if _, ok := m.mcpStates[mcpCfg.Name]; ok {
			mcpsCount++
		}
	}

	skillsCount := len(m.skillStatusItems())

	maxFiles, maxLSPs, maxMCPs, maxSkills := getDynamicHeightLimits(remainingHeight, filesCount, lspsCount, mcpsCount, skillsCount)

	lspSection := m.lspInfo(width, maxLSPs, true)
	mcpSection := m.mcpInfo(width, maxMCPs, true)
	skillsSection := m.skillsInfo(width, maxSkills, true)
	filesSection := m.filesInfo(m.com.Workspace.WorkingDir(), width, maxFiles, true)

	uv.NewStyledString(
		lipgloss.NewStyle().
			MaxWidth(width).
			MaxHeight(height).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Left,
					sidebarHeader,
					filesSection,
					"",
					lspSection,
					"",
					mcpSection,
					"",
					skillsSection,
				),
			),
	).Draw(scr, area)
}

package model

import (
	"cmp"
	"fmt"
	"image"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	mcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
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
			ContextUsed:    activeSession.CompletionTokens + activeSession.PromptTokens,
			Cost:           activeSession.Cost,
			ModelContext:   model.CatwalkCfg.ContextWindow,
			EstimatedUsage: activeSession.EstimatedUsage,
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

// sidebarMaxOffset returns the maximum sidebar scroll offset based on
// the last drawn content height. The value is computed during drawSidebar.
func (m *UI) sidebarMaxOffset() int {
	return m.sidebarMaxOffsetVal
}

// drawSidebar renders the chat sidebar with a fixed logo and a
// virtual-scrolling content area with an auto-hiding scrollbar. While the
// sidebar is focused, the scrollbar stays visible.
func (m *UI) drawSidebar(scr uv.Screen, area uv.Rectangle) {
	if m.session == nil {
		return
	}

	const logoHeightBreakpoint = 30

	t := m.com.Styles
	width := area.Dx()
	height := area.Dy()

	contentWidth := max(width-2, 1)

	title := t.Sidebar.SessionTitle.Width(contentWidth).MaxHeight(2).Render(m.session.Title)
	cwd := common.PrettyPath(t, m.com.Workspace.WorkingDir(), contentWidth)
	sidebarLogo := m.sidebarLogo
	if height < logoHeightBreakpoint {
		sidebarLogo = lipgloss.JoinVertical(lipgloss.Left, logo.SmallRender(m.com.Styles, contentWidth, logo.Opts{
			RandomColor: true,
		}), "")
	}
	var logoRect, contentRect image.Rectangle
	layout.Vertical(
		layout.Len(lipgloss.Height(sidebarLogo)),
		layout.Fill(1),
	).Split(area).Assign(&logoRect, &contentRect)

	contentHeight := contentRect.Dy()

	// Render all items without truncation; virtual scrolling handles overflow.
	lspSection := m.lspInfo(contentWidth, len(m.lspStates), true)
	mcpSection := m.mcpInfo(contentWidth, mcpCount(m.com.Config().MCP.Sorted(), m.mcpStates), true)
	skillsSection := m.skillsInfo(contentWidth, len(m.skillStatusItems()), true)
	filesSection := m.filesInfo(m.com.Workspace.WorkingDir(), contentWidth, fileChangeCount(m.sessionFiles), true)

	// Build the scrollable content.
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		cwd,
		"",
		m.modelInfo(contentWidth),
		"",
		filesSection,
		"",
		lspSection,
		"",
		mcpSection,
		"",
		skillsSection,
	)

	// Split into lines for virtual scrolling.
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	m.sidebarScrollable = totalLines > contentHeight
	m.sidebarMaxOffsetVal = max(0, totalLines-contentHeight)

	// If the sidebar is focused but no longer scrollable (e.g. after a
	// resize), return focus to the chat.
	if m.focus == uiFocusSidebar && !m.sidebarScrollable {
		m.focus = uiFocusMain
		m.activeChat().Focus()
	}

	// Clamp sidebarOffset.
	maxOffset := m.sidebarMaxOffsetVal
	if m.sidebarOffset > maxOffset {
		m.sidebarOffset = maxOffset
	}

	// Slice visible lines.
	end := min(m.sidebarOffset+contentHeight, totalLines)
	visibleLines := lines[m.sidebarOffset:end]
	visibleStr := strings.Join(visibleLines, "\n")

	// Determine scrollbar visibility: always visible when focused, otherwise
	// auto-hide.
	scrollbarVisible := totalLines > contentHeight && (m.sidebarScrollbarVisible || m.focus == uiFocusSidebar)

	// Draw the fixed logo.
	uv.NewStyledString(
		lipgloss.NewStyle().
			MaxWidth(contentWidth).
			MaxHeight(lipgloss.Height(sidebarLogo)).
			Render(sidebarLogo),
	).Draw(scr, logoRect)

	// Draw the visible content in the scrollable area.
	uv.NewStyledString(
		lipgloss.NewStyle().
			MaxWidth(contentWidth).
			MaxHeight(contentHeight).
			Render(visibleStr),
	).Draw(scr, contentRect)

	// Draw scrollbar in the reserved column.
	if scrollbarVisible {
		scrollbar := common.Scrollbar(m.com.Styles, contentHeight, totalLines, contentHeight, m.sidebarOffset)
		if scrollbar != "" {
			scrollbarArea := image.Rectangle{
				Min: image.Point{X: area.Max.X - 1, Y: contentRect.Min.Y},
				Max: image.Point{X: area.Max.X, Y: area.Max.Y},
			}
			uv.NewStyledString(scrollbar).Draw(scr, scrollbarArea)
		}
	}
}

// fileChangeCount returns the number of session files with non-zero additions
// or deletions.
func fileChangeCount(files []SessionFile) int {
	count := 0
	for _, f := range files {
		if f.Additions == 0 && f.Deletions == 0 {
			continue
		}
		count++
	}
	return count
}

// mcpCount returns the number of MCP servers that have a state entry.
func mcpCount(mcpCfgs []config.MCP, states map[string]mcp.ClientInfo) int {
	count := 0
	for _, cfg := range mcpCfgs {
		if _, ok := states[cfg.Name]; ok {
			count++
		}
	}
	return count
}

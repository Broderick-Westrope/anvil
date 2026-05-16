package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/anim"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
)

// -----------------------------------------------------------------------------
// Agent Tool
// -----------------------------------------------------------------------------

// NestedToolContainer is an interface for tool items that can contain nested tool calls.
type NestedToolContainer interface {
	NestedTools() []ToolMessageItem
	SetNestedTools(tools []ToolMessageItem)
	AddNestedTool(tool ToolMessageItem)
}

// AgentToolMessageItem is a message item that represents an agent tool call.
type AgentToolMessageItem struct {
	*baseToolMessageItem

	nestedTools []ToolMessageItem

	// Drill-in state.
	childSessionID   string
	hasChildMessages bool

	// Live stats updated via pubsub as child session messages arrive.
	turns          int
	toolCalls      int
	tokens         int64
	cost           float64
	countedToolIDs map[string]bool // track already-counted tool call IDs.
}

var (
	_ ToolMessageItem     = (*AgentToolMessageItem)(nil)
	_ NestedToolContainer = (*AgentToolMessageItem)(nil)
	_ DrillInHandler      = (*AgentToolMessageItem)(nil)
	_ KeyEventHandler     = (*AgentToolMessageItem)(nil)
)

// NewAgentToolMessageItem creates a new [AgentToolMessageItem].
func NewAgentToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) *AgentToolMessageItem {
	t := &AgentToolMessageItem{}
	t.baseToolMessageItem = newBaseToolMessageItem(sty, toolCall, result, &AgentToolRenderContext{agent: t}, canceled)
	// For the agent tool we keep spinning until the tool call is finished.
	t.spinningFunc = func(state SpinningState) bool {
		return !state.HasResult() && !state.IsCanceled()
	}
	// Use a single-char shimmer animation for agent items.
	t.anim = anim.New(anim.Settings{
		ID:   t.ID(),
		Size: 1,
	})
	return t
}

// Animate progresses the message animation if it should be spinning.
func (a *AgentToolMessageItem) Animate(msg anim.StepMsg) tea.Cmd {
	if a.result != nil || a.Status() == ToolStatusCanceled {
		return nil
	}
	if msg.ID == a.ID() {
		return a.anim.Animate(msg)
	}
	return nil
}

// NestedTools returns the nested tools.
func (a *AgentToolMessageItem) NestedTools() []ToolMessageItem {
	return a.nestedTools
}

// SetNestedTools sets the nested tools.
func (a *AgentToolMessageItem) SetNestedTools(tools []ToolMessageItem) {
	a.nestedTools = tools
	a.clearCache()
}

// AddNestedTool adds a nested tool.
func (a *AgentToolMessageItem) AddNestedTool(tool ToolMessageItem) {
	// Mark nested tools as simple (compact) rendering.
	if s, ok := tool.(Compactable); ok {
		s.SetCompact(true)
	}
	a.nestedTools = append(a.nestedTools, tool)
	a.clearCache()
}

// DrillIn returns the child session ID to drill into.
func (a *AgentToolMessageItem) DrillIn() string {
	return a.childSessionID
}

// DrillInLabel returns the breadcrumb label for this item.
func (a *AgentToolMessageItem) DrillInLabel() string {
	var params agent.TaskParams
	_ = json.Unmarshal([]byte(a.toolCall.Input), &params)
	return agentBreadcrumbLabel(params.SubagentType, params.Description)
}

// SetChildSessionID sets the child session ID for drill-in navigation.
func (a *AgentToolMessageItem) SetChildSessionID(id string) {
	a.childSessionID = id
}

// SetHasChildMessages sets whether the child session has received messages.
func (a *AgentToolMessageItem) SetHasChildMessages(v bool) {
	a.hasChildMessages = v
	a.clearCache()
}

// Stats returns the current turn and tool call counts.
func (a *AgentToolMessageItem) Stats() (turns, toolCalls int) {
	return a.turns, a.toolCalls
}

// IncrementTurns increments the turn count and clears the render cache.
func (a *AgentToolMessageItem) IncrementTurns() {
	a.turns++
	a.clearCache()
}

// IncrementToolCalls increments the tool call count and clears the render cache.
func (a *AgentToolMessageItem) IncrementToolCalls(n int) {
	a.toolCalls += n
	a.clearCache()
}

// SetTokens sets the token count and clears the render cache.
func (a *AgentToolMessageItem) SetTokens(t int64) {
	a.tokens = t
	a.clearCache()
}

// SetCost sets the cost and clears the render cache.
func (a *AgentToolMessageItem) SetCost(c float64) {
	a.cost = c
	a.clearCache()
}

// CountedToolIDs returns the map of already-counted tool call IDs, initialising
// it lazily.
func (a *AgentToolMessageItem) CountedToolIDs() map[string]bool {
	if a.countedToolIDs == nil {
		a.countedToolIDs = make(map[string]bool)
	}
	return a.countedToolIDs
}

// HandleKeyEvent implements [KeyEventHandler]. It handles the → key for
// drill-in navigation when a child session is available, and delegates other
// keys to the base handler.
func (a *AgentToolMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if key.String() == "right" && a.childSessionID != "" {
		return true, func() tea.Msg {
			return util.DrillInMsg{
				SessionID: a.childSessionID,
				Label:     a.DrillInLabel(),
			}
		}
	}
	return a.baseToolMessageItem.HandleKeyEvent(key)
}

// AgentToolRenderContext renders agent tool messages.
type AgentToolRenderContext struct {
	agent *AgentToolMessageItem
}

// RenderTool implements the [ToolRenderer] interface.
func (r *AgentToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)

	var params agent.TaskParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	displayName := agentDisplayName(params.SubagentType, params.Description, params.Model)

	// Line 1: status icon + display name.
	header := toolHeader(sty, opts.Status, displayName, cappedWidth, opts.Compact)
	if opts.Compact {
		return header
	}

	// Line 2: live stats (turns, tools, tokens, cost, elapsed).
	statsLine := formatStatsLine(sty, r.agent.turns, r.agent.toolCalls, r.agent.tokens, r.agent.cost, "", cappedWidth)

	return lipgloss.JoinVertical(lipgloss.Left, header, statsLine)
}

// agentDisplayName returns a human-readable name for a task tool call.
// It capitalises the subagent type (e.g. "explorer" → "Explorer") and
// appends a dash-separated description when available (e.g.
// "Explorer — Search auth middleware"). Falls back to the description
// alone or "Unknown Agent" when the type is empty. When model is
// non-empty, a short model identifier is appended in parentheses
// (e.g. "Reviewer (opus-4-6)").
func agentDisplayName(subagentType, description, model string) string {
	var name string
	if subagentType != "" {
		// Capitalise each word and replace hyphens with spaces
		// (e.g. "devils-advocate" → "Devils Advocate").
		parts := strings.Split(subagentType, "-")
		for i, p := range parts {
			if len(p) > 0 {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		name = strings.Join(parts, " ")
	} else {
		name = "Unknown Agent"
	}

	// Append a short model identifier to the agent name so the user
	// sees who is running (e.g. "Reviewer (opus-4-6)").
	if model != "" {
		shortModel := model
		if idx := strings.LastIndex(model, "/"); idx >= 0 {
			shortModel = model[idx+1:]
		}
		// Strip common "claude-" prefix for brevity.
		shortModel = strings.TrimPrefix(shortModel, "claude-")
		name = name + " (" + shortModel + ")"
	}

	// Append the task description after the identity so the user sees
	// what the agent is doing (e.g. "Explorer (opus-4-6) — Search auth").
	if description != "" {
		name = name + " — " + description
	}

	return name
}

// agentBreadcrumbLabel builds a breadcrumb label like "Explorer: Search auth".
// It capitalises the subagent type and appends the description. Falls back to
// "Agent" when the type is empty.
func agentBreadcrumbLabel(subagentType, description string) string {
	var name string
	if subagentType != "" {
		parts := strings.Split(subagentType, "-")
		for i, p := range parts {
			if len(p) > 0 {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		name = strings.Join(parts, " ")
	} else {
		name = "Agent"
	}
	if description != "" {
		name += ": " + description
	}
	return name
}

// formatStatsLine formats the stats line for a collapsed agent tool view.
// The format is: "  3 turns · 12 tools · 4.2k tokens · $0.02 · 14s"
// For narrow widths (<80), it abbreviates: "  3t · 12tl · 4.2k · $0.02 · 14s".
func formatStatsLine(sty *styles.Styles, turns, toolCalls int, tokens int64, cost float64, elapsed string, width int) string {
	sep := sty.Tool.StatsSep.Render(" · ")
	narrow := width < 80

	var parts []string
	if narrow {
		parts = append(parts, sty.Tool.StatsLine.Render(fmt.Sprintf("%dt", turns)))
		parts = append(parts, sty.Tool.StatsLine.Render(fmt.Sprintf("%dtl", toolCalls)))
	} else {
		parts = append(parts, sty.Tool.StatsLine.Render(fmt.Sprintf("%d turns", turns)))
		parts = append(parts, sty.Tool.StatsLine.Render(fmt.Sprintf("%d tools", toolCalls)))
	}

	if tokenStr := formatAgentTokens(tokens, narrow); tokenStr != "" {
		parts = append(parts, sty.Tool.StatsLine.Render(tokenStr))
	}

	if cost > 0 {
		parts = append(parts, sty.Tool.StatsLine.Render(fmt.Sprintf("$%.2f", cost)))
	}

	if elapsed != "" {
		parts = append(parts, sty.Tool.StatsLine.Render(elapsed))
	}

	return "  " + strings.Join(parts, sep)
}

// formatAgentTokens formats a token count with k/M suffixes. Returns an empty
// string for zero. For abbreviated mode (narrow terminal), the "tokens" label
// is omitted.
func formatAgentTokens(tokens int64, abbreviated bool) string {
	if tokens == 0 {
		return ""
	}
	var num string
	switch {
	case tokens >= 1_000_000:
		num = fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1000:
		num = fmt.Sprintf("%.1fk", float64(tokens)/1000)
	default:
		num = fmt.Sprintf("%d", tokens)
	}
	if abbreviated {
		return num
	}
	return num + " tokens"
}

// -----------------------------------------------------------------------------
// Agentic Fetch Tool
// -----------------------------------------------------------------------------

// AgenticFetchToolMessageItem is a message item that represents an agentic fetch tool call.
type AgenticFetchToolMessageItem struct {
	*baseToolMessageItem

	nestedTools []ToolMessageItem

	// Drill-in state.
	childSessionID   string
	hasChildMessages bool

	// Live stats updated via pubsub as child session messages arrive.
	turns          int
	toolCalls      int
	tokens         int64
	cost           float64
	countedToolIDs map[string]bool // track already-counted tool call IDs.
}

var (
	_ ToolMessageItem     = (*AgenticFetchToolMessageItem)(nil)
	_ NestedToolContainer = (*AgenticFetchToolMessageItem)(nil)
	_ DrillInHandler      = (*AgenticFetchToolMessageItem)(nil)
	_ KeyEventHandler     = (*AgenticFetchToolMessageItem)(nil)
)

// NewAgenticFetchToolMessageItem creates a new [AgenticFetchToolMessageItem].
func NewAgenticFetchToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) *AgenticFetchToolMessageItem {
	t := &AgenticFetchToolMessageItem{}
	t.baseToolMessageItem = newBaseToolMessageItem(sty, toolCall, result, &AgenticFetchToolRenderContext{fetch: t}, canceled)
	// For the agentic fetch tool we keep spinning until the tool call is finished.
	t.spinningFunc = func(state SpinningState) bool {
		return !state.HasResult() && !state.IsCanceled()
	}
	// Use a single-char shimmer animation for agent items.
	t.anim = anim.New(anim.Settings{
		ID:   t.ID(),
		Size: 1,
	})
	return t
}

// NestedTools returns the nested tools.
func (a *AgenticFetchToolMessageItem) NestedTools() []ToolMessageItem {
	return a.nestedTools
}

// SetNestedTools sets the nested tools.
func (a *AgenticFetchToolMessageItem) SetNestedTools(tools []ToolMessageItem) {
	a.nestedTools = tools
	a.clearCache()
}

// AddNestedTool adds a nested tool.
func (a *AgenticFetchToolMessageItem) AddNestedTool(tool ToolMessageItem) {
	// Mark nested tools as simple (compact) rendering.
	if s, ok := tool.(Compactable); ok {
		s.SetCompact(true)
	}
	a.nestedTools = append(a.nestedTools, tool)
	a.clearCache()
}

// DrillIn returns the child session ID to drill into.
func (a *AgenticFetchToolMessageItem) DrillIn() string {
	return a.childSessionID
}

// DrillInLabel returns the breadcrumb label for this item.
func (a *AgenticFetchToolMessageItem) DrillInLabel() string {
	var params agenticFetchParams
	_ = json.Unmarshal([]byte(a.toolCall.Input), &params)
	prompt := ansi.Truncate(params.Prompt, 40, "…")
	return "Fetch: " + prompt
}

// SetChildSessionID sets the child session ID for drill-in navigation.
func (a *AgenticFetchToolMessageItem) SetChildSessionID(id string) {
	a.childSessionID = id
}

// SetHasChildMessages sets whether the child session has received messages.
func (a *AgenticFetchToolMessageItem) SetHasChildMessages(v bool) {
	a.hasChildMessages = v
	a.clearCache()
}

// Stats returns the current turn and tool call counts.
func (a *AgenticFetchToolMessageItem) Stats() (turns, toolCalls int) {
	return a.turns, a.toolCalls
}

// IncrementTurns increments the turn count and clears the render cache.
func (a *AgenticFetchToolMessageItem) IncrementTurns() {
	a.turns++
	a.clearCache()
}

// IncrementToolCalls increments the tool call count and clears the render cache.
func (a *AgenticFetchToolMessageItem) IncrementToolCalls(n int) {
	a.toolCalls += n
	a.clearCache()
}

// SetTokens sets the token count and clears the render cache.
func (a *AgenticFetchToolMessageItem) SetTokens(t int64) {
	a.tokens = t
	a.clearCache()
}

// SetCost sets the cost and clears the render cache.
func (a *AgenticFetchToolMessageItem) SetCost(c float64) {
	a.cost = c
	a.clearCache()
}

// CountedToolIDs returns the map of already-counted tool call IDs, initialising
// it lazily.
func (a *AgenticFetchToolMessageItem) CountedToolIDs() map[string]bool {
	if a.countedToolIDs == nil {
		a.countedToolIDs = make(map[string]bool)
	}
	return a.countedToolIDs
}

// HandleKeyEvent implements [KeyEventHandler]. It handles the → key for
// drill-in navigation when a child session is available, and delegates other
// keys to the base handler.
func (a *AgenticFetchToolMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if key.String() == "right" && a.childSessionID != "" {
		return true, func() tea.Msg {
			return util.DrillInMsg{
				SessionID: a.childSessionID,
				Label:     a.DrillInLabel(),
			}
		}
	}
	return a.baseToolMessageItem.HandleKeyEvent(key)
}

// AgenticFetchToolRenderContext renders agentic fetch tool messages.
type AgenticFetchToolRenderContext struct {
	fetch *AgenticFetchToolMessageItem
}

// agenticFetchParams matches tools.AgenticFetchParams.
type agenticFetchParams struct {
	URL    string `json:"url,omitempty"`
	Prompt string `json:"prompt"`
}

// RenderTool implements the [ToolRenderer] interface.
func (r *AgenticFetchToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)

	var params agenticFetchParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	// Build header with optional URL param.
	var toolParams []string
	if params.URL != "" {
		toolParams = append(toolParams, params.URL)
	}

	// Line 1: status icon + display name with optional URL.
	header := toolHeader(sty, opts.Status, "Agentic Fetch", cappedWidth, opts.Compact, toolParams...)
	if opts.Compact {
		return header
	}

	// Line 2: live stats (turns, tools, tokens, cost, elapsed).
	statsLine := formatStatsLine(sty, r.fetch.turns, r.fetch.toolCalls, r.fetch.tokens, r.fetch.cost, "", cappedWidth)

	return lipgloss.JoinVertical(lipgloss.Left, header, statsLine)
}

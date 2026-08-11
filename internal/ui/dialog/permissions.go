package dialog

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/fsext"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
	"github.com/Broderick-Westrope/anvil/internal/stringext"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
)

// PermissionsID is the identifier for the permissions dialog.
const PermissionsID = "permissions"

// PermissionAction represents the user's response to a permission request.
type PermissionAction string

const (
	PermissionAllow           PermissionAction = "allow"
	PermissionAllowForSession PermissionAction = "allow_session"
	PermissionAllowForever    PermissionAction = "allow_forever"
	PermissionDeny            PermissionAction = "deny"
)

// Permissions dialog sizing constants.
const (
	// diffMaxWidth is the maximum width for diff views.
	diffMaxWidth = 180
	// diffSizeRatio is the size ratio for diff views relative to window.
	diffSizeRatio = 0.8
	// simpleMaxWidth is the maximum width for simple content dialogs.
	simpleMaxWidth = 100
	// simpleSizeRatio is the size ratio for simple content dialogs.
	simpleSizeRatio = 0.6
	// simpleHeightRatio is the height ratio for simple content dialogs.
	simpleHeightRatio = 0.5
	// splitModeMinWidth is the minimum width to enable split diff mode.
	splitModeMinWidth = 140
	// layoutSpacingLines is the number of empty lines used for layout spacing.
	layoutSpacingLines = 4
	// minWindowWidth is the minimum window width before forcing fullscreen.
	minWindowWidth = 77
	// minWindowHeight is the minimum window height before forcing fullscreen.
	minWindowHeight = 20
)

// Permissions represents a dialog for permission requests.
type Permissions struct {
	com          *common.Common
	windowWidth  int // Terminal window dimensions.
	windowHeight int
	fullscreen   bool // true when dialog is fullscreen

	permission     permission.PermissionRequest
	selectedOption int // 0: Allow, 1: Session, 2: Forever, 3: Deny

	// Forever sub-choice state.
	foreverExpanded bool         // Whether the forever scope picker is showing.
	foreverScope    config.Scope // Selected scope for forever grants.

	// Deny reason state.
	denyReasonVisible bool   // Whether the deny reason input is showing.
	denyReasonInput   string // The deny reason text.

	// Pattern editing state.
	patternInput   textinput.Model // Editable glob pattern for session/forever grants.
	patternFocused bool            // Whether the pattern field has keyboard focus.

	viewport      viewport.Model
	viewportDirty bool // true when viewport content needs to be re-rendered
	viewportWidth int

	// Diff view state.
	diffSplitMode        *bool // nil means use default based on width
	defaultDiffSplitMode bool  // default split mode based on width
	diffXOffset          int   // horizontal scroll offset for diff view
	unifiedDiffContent   string
	splitDiffContent     string

	help   help.Model
	keyMap permissionsKeyMap
}

type permissionsKeyMap struct {
	Left             key.Binding
	Right            key.Binding
	Tab              key.Binding
	Select           key.Binding
	Allow            key.Binding
	AllowSession     key.Binding
	AllowForever     key.Binding
	Deny             key.Binding
	Close            key.Binding
	EditPattern      key.Binding
	ToggleDiffMode   key.Binding
	ToggleFullscreen key.Binding
	ScrollUp         key.Binding
	ScrollDown       key.Binding
	ScrollLeft       key.Binding
	ScrollRight      key.Binding
	Choose           key.Binding
	Scroll           key.Binding
}

func defaultPermissionsKeyMap() permissionsKeyMap {
	return permissionsKeyMap{
		Left: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←", "previous"),
		),
		Right: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→", "next"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next option"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter", "ctrl+y"),
			key.WithHelp("enter", "confirm"),
		),
		Allow: key.NewBinding(
			key.WithKeys("a", "A", "ctrl+a"),
			key.WithHelp("a", "allow"),
		),
		AllowSession: key.NewBinding(
			key.WithKeys("s", "S", "ctrl+s"),
			key.WithHelp("s", "allow session"),
		),
		AllowForever: key.NewBinding(
			key.WithKeys("f", "F"),
			key.WithHelp("f", "allow forever"),
		),
		Deny: key.NewBinding(
			key.WithKeys("d", "D"),
			key.WithHelp("d", "deny"),
		),
		Close: CloseKey,
		EditPattern: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit pattern"),
		),
		ToggleDiffMode: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "toggle diff view"),
		),
		ToggleFullscreen: key.NewBinding(
			key.WithKeys("ctrl+f"),
			key.WithHelp("ctrl+f", "toggle fullscreen"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("shift+up", "K"),
			key.WithHelp("shift+↑", "scroll up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("shift+down", "J"),
			key.WithHelp("shift+↓", "scroll down"),
		),
		ScrollLeft: key.NewBinding(
			key.WithKeys("shift+left", "H"),
			key.WithHelp("shift+←", "scroll left"),
		),
		ScrollRight: key.NewBinding(
			key.WithKeys("shift+right", "L"),
			key.WithHelp("shift+→", "scroll right"),
		),
		Choose: key.NewBinding(
			key.WithKeys("left", "right"),
			key.WithHelp("←/→", "choose"),
		),
		Scroll: key.NewBinding(
			key.WithKeys("shift+left", "shift+down", "shift+up", "shift+right"),
			key.WithHelp("shift+←↓↑→", "scroll"),
		),
	}
}

var _ Dialog = (*Permissions)(nil)

// PermissionsOption configures the permissions dialog.
type PermissionsOption func(*Permissions)

// WithDiffMode sets the initial diff mode (split or unified).
func WithDiffMode(split bool) PermissionsOption {
	return func(p *Permissions) {
		p.diffSplitMode = &split
	}
}

// NewPermissions creates a new permissions dialog.
func NewPermissions(com *common.Common, perm permission.PermissionRequest, opts ...PermissionsOption) *Permissions {
	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()

	km := defaultPermissionsKeyMap()

	// Configure viewport with matching keybindings.
	vp := viewport.New()
	vp.KeyMap = viewport.KeyMap{
		Up:    km.ScrollUp,
		Down:  km.ScrollDown,
		Left:  km.ScrollLeft,
		Right: km.ScrollRight,
		// Disable other viewport keys to avoid conflicts with dialog shortcuts.
		PageUp:       key.NewBinding(key.WithDisabled()),
		PageDown:     key.NewBinding(key.WithDisabled()),
		HalfPageUp:   key.NewBinding(key.WithDisabled()),
		HalfPageDown: key.NewBinding(key.WithDisabled()),
	}

	p := &Permissions{
		com:            com,
		permission:     perm,
		selectedOption: 0,
		viewport:       vp,
		help:           h,
		keyMap:         km,
	}

	p.patternInput = textinput.New()
	p.patternInput.SetVirtualCursor(false)
	p.patternInput.Placeholder = "Edit pattern (glob)"
	p.patternInput.SetStyles(com.Styles.TextInput)
	if len(perm.InputSegments) > 0 {
		patterns := make([]string, 0, len(perm.InputSegments))
		for _, seg := range perm.InputSegments {
			patterns = append(patterns, segment.Generalize(seg))
		}
		p.patternInput.SetValue(strings.Join(patterns, " && "))
	} else {
		p.patternInput.SetValue(perm.Input)
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Calculate usable content width (dialog border + horizontal padding).
func (p *Permissions) calculateContentWidth(width int) int {
	t := p.com.Styles
	const dialogHorizontalPadding = 2
	return width - t.Dialog.View.GetHorizontalFrameSize() - dialogHorizontalPadding
}

// ID implements [Dialog].
func (*Permissions) ID() string {
	return PermissionsID
}

// HandleMsg implements [Dialog].
func (p *Permissions) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Pattern input focused: forward keys to text input.
		if p.patternFocused {
			return p.handlePatternMsg(msg)
		}

		// Forever-expanded state: choose project or user scope.
		if p.foreverExpanded {
			return p.handleForeverMsg(msg)
		}

		// Deny-reason state: text input for denial reason.
		if p.denyReasonVisible {
			return p.handleDenyReasonMsg(msg)
		}

		// Default state.
		return p.handleDefaultMsg(msg)
	case common.CoalescedWheelMsg:
		if p.hasDiffView() {
			if msg.DeltaX < 0 {
				p.scrollLeft()
			} else if msg.DeltaX > 0 {
				p.scrollRight()
			} else {
				p.viewport, _ = p.viewport.Update(tea.MouseWheelMsg(msg.Mouse))
			}
		} else {
			p.viewport, _ = p.viewport.Update(tea.MouseWheelMsg(msg.Mouse))
		}
	default:
		// Pass unhandled keys to viewport for non-diff content scrolling.
		if !p.hasDiffView() {
			p.viewport, _ = p.viewport.Update(msg)
			p.viewportDirty = true
		}
	}

	return nil
}

func (p *Permissions) handleDefaultMsg(msg tea.KeyPressMsg) Action {
	switch {
	case key.Matches(msg, p.keyMap.Close):
		// Escape denies: the agent is blocked awaiting a decision, so
		// dismissing the dialog must resolve the request, and deny is
		// the only safe resolution. This intentionally differs from
		// the sub-states (forever scope, deny reason) where Escape
		// backs out to this state instead.
		return p.respond(PermissionDeny)
	case key.Matches(msg, p.keyMap.Right), key.Matches(msg, p.keyMap.Tab):
		p.selectedOption = (p.selectedOption + 1) % 4
	case key.Matches(msg, p.keyMap.Left):
		// Add 3 instead of subtracting 1 to avoid negative modulo.
		p.selectedOption = (p.selectedOption + 3) % 4
	case key.Matches(msg, p.keyMap.Select):
		return p.selectCurrentOption()
	case key.Matches(msg, p.keyMap.Allow):
		return p.respond(PermissionAllow)
	case key.Matches(msg, p.keyMap.AllowSession):
		return p.respond(PermissionAllowForSession)
	case key.Matches(msg, p.keyMap.AllowForever):
		p.foreverExpanded = true
		p.selectedOption = 0
	case key.Matches(msg, p.keyMap.Deny):
		p.denyReasonVisible = true
		p.denyReasonInput = ""
	case msg.Text == "e" || msg.Text == "E":
		p.patternFocused = true
		p.patternInput.Focus()
		p.patternInput.CursorEnd()
	case key.Matches(msg, p.keyMap.ToggleDiffMode):
		if p.hasDiffView() {
			newMode := !p.isSplitMode()
			p.diffSplitMode = &newMode
			p.viewportDirty = true
		}
	case key.Matches(msg, p.keyMap.ToggleFullscreen):
		if p.hasDiffView() {
			p.fullscreen = !p.fullscreen
		}
	case key.Matches(msg, p.keyMap.ScrollDown):
		p.viewport, _ = p.viewport.Update(msg)
	case key.Matches(msg, p.keyMap.ScrollUp):
		p.viewport, _ = p.viewport.Update(msg)
	case key.Matches(msg, p.keyMap.ScrollLeft):
		if p.hasDiffView() {
			p.scrollLeft()
		} else {
			p.viewport, _ = p.viewport.Update(msg)
		}
	case key.Matches(msg, p.keyMap.ScrollRight):
		if p.hasDiffView() {
			p.scrollRight()
		} else {
			p.viewport, _ = p.viewport.Update(msg)
		}
	}
	return nil
}

func (p *Permissions) handleForeverMsg(msg tea.KeyPressMsg) Action {
	switch {
	case key.Matches(msg, p.keyMap.Close):
		// Return to default state.
		p.foreverExpanded = false
		p.selectedOption = 2
	case key.Matches(msg, p.keyMap.Right), key.Matches(msg, p.keyMap.Tab):
		p.selectedOption = (p.selectedOption + 1) % 2
	case key.Matches(msg, p.keyMap.Left):
		p.selectedOption = (p.selectedOption + 1) % 2
	case key.Matches(msg, p.keyMap.Select):
		if p.selectedOption == 0 {
			p.foreverScope = config.ScopeWorkspace
		} else {
			p.foreverScope = config.ScopeGlobal
		}
		return p.respond(PermissionAllowForever)
	case msg.Text == "p" || msg.Text == "P":
		p.foreverScope = config.ScopeWorkspace
		return p.respond(PermissionAllowForever)
	case msg.Text == "u" || msg.Text == "U":
		p.foreverScope = config.ScopeGlobal
		return p.respond(PermissionAllowForever)
	}
	return nil
}

func (p *Permissions) handlePatternMsg(msg tea.KeyPressMsg) Action {
	switch {
	case key.Matches(msg, p.keyMap.Close), msg.Code == tea.KeyEnter:
		// Unfocus pattern input, return to button navigation.
		p.patternFocused = false
		p.patternInput.Blur()
	default:
		// Forward to text input.
		p.patternInput, _ = p.patternInput.Update(msg)
	}
	return nil
}

func (p *Permissions) handleDenyReasonMsg(msg tea.KeyPressMsg) Action {
	switch {
	case key.Matches(msg, p.keyMap.Close):
		// Return to default state without denying.
		p.denyReasonVisible = false
		p.denyReasonInput = ""
		p.selectedOption = 3
	case msg.Code == tea.KeyEnter:
		return p.respond(PermissionDeny)
	case msg.Code == tea.KeyBackspace:
		if len(p.denyReasonInput) > 0 {
			runes := []rune(p.denyReasonInput)
			p.denyReasonInput = string(runes[:len(runes)-1])
		}
	default:
		if msg.Text != "" {
			p.denyReasonInput += msg.Text
		}
	}
	return nil
}

func (p *Permissions) selectCurrentOption() tea.Msg {
	switch p.selectedOption {
	case 0:
		return p.respond(PermissionAllow)
	case 1:
		return p.respond(PermissionAllowForSession)
	case 2:
		p.foreverExpanded = true
		p.selectedOption = 0
		return nil
	default:
		p.denyReasonVisible = true
		p.denyReasonInput = ""
		return nil
	}
}

func (p *Permissions) respond(action PermissionAction) tea.Msg {
	return ActionPermissionResponse{
		Permission: p.permission,
		Action:     action,
		Pattern:    p.patternInput.Value(),
		Scope:      p.foreverScope,
		Reason:     p.denyReasonInput,
	}
}

func (p *Permissions) hasDiffView() bool {
	switch p.permission.ToolName {
	case tools.EditToolName, tools.WriteToolName, tools.MultiEditToolName:
		return true
	}
	return false
}

func (p *Permissions) isSplitMode() bool {
	if p.diffSplitMode != nil {
		return *p.diffSplitMode
	}
	return p.defaultDiffSplitMode
}

const horizontalScrollStep = 5

func (p *Permissions) scrollLeft() {
	p.diffXOffset = max(0, p.diffXOffset-horizontalScrollStep)
	p.viewportDirty = true
}

func (p *Permissions) scrollRight() {
	p.diffXOffset += horizontalScrollStep
	p.viewportDirty = true
}

// Draw implements [Dialog].
func (p *Permissions) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := p.com.Styles
	// Force fullscreen when window is too small.
	forceFullscreen := area.Dx() <= minWindowWidth || area.Dy() <= minWindowHeight

	// Calculate dialog dimensions based on fullscreen state and content type.
	var width, maxHeight int
	if forceFullscreen || (p.fullscreen && p.hasDiffView()) {
		// Use nearly full window for fullscreen.
		width = area.Dx()
		maxHeight = area.Dy()
	} else if p.hasDiffView() {
		// Wide for side-by-side diffs, capped for readability.
		width = min(int(float64(area.Dx())*diffSizeRatio), diffMaxWidth)
		maxHeight = int(float64(area.Dy()) * diffSizeRatio)
	} else {
		// Narrower for simple content like commands/URLs.
		width = min(int(float64(area.Dx())*simpleSizeRatio), simpleMaxWidth)
		maxHeight = int(float64(area.Dy()) * simpleHeightRatio)
	}

	dialogStyle := t.Dialog.View.Width(width).Padding(0, 1)

	contentWidth := p.calculateContentWidth(width)
	header := p.renderHeader(contentWidth)
	buttons := p.renderButtons(contentWidth)
	// Pack the hints to the content width so they truncate cleanly instead
	// of overflowing. The dialog frame supplies the padding, so this renders
	// the hint line without the extra help view inset that renderDialogHelp
	// applies for RenderContext dialogs.
	helpView := shortHelpLine(&p.help, p.ShortHelp(), contentWidth)

	// Calculate available height for content.
	headerHeight := lipgloss.Height(header)
	buttonsHeight := lipgloss.Height(buttons)
	helpHeight := lipgloss.Height(helpView)
	frameHeight := dialogStyle.GetVerticalFrameSize() + layoutSpacingLines

	p.defaultDiffSplitMode = width >= splitModeMinWidth

	// Pre-render content to measure its actual height.
	renderedContent := p.renderContent(contentWidth)
	contentHeight := lipgloss.Height(renderedContent)

	// For non-diff views, shrink dialog to fit content if it's smaller than max.
	var availableHeight int
	if !p.hasDiffView() && !forceFullscreen {
		patternHeight := lipgloss.Height(p.renderPatternInput(contentWidth))
		if patternHeight > 0 {
			patternHeight += 1 // Account for the blank line separator.
		}
		fixedHeight := headerHeight + buttonsHeight + helpHeight + frameHeight + patternHeight
		neededHeight := fixedHeight + contentHeight
		if neededHeight < maxHeight {
			availableHeight = contentHeight
		} else {
			availableHeight = maxHeight - fixedHeight
		}
		availableHeight = max(availableHeight, 3)
	} else {
		patternHeight := lipgloss.Height(p.renderPatternInput(contentWidth))
		if patternHeight > 0 {
			patternHeight += 1 // Account for the blank line separator.
		}
		availableHeight = maxHeight - headerHeight - buttonsHeight - helpHeight - frameHeight - patternHeight
	}

	// Determine if scrollbar is needed.
	needsScrollbar := p.hasDiffView() || contentHeight > availableHeight
	viewportWidth := contentWidth
	if needsScrollbar {
		viewportWidth = contentWidth - 1 // Reserve space for scrollbar.
	}

	if p.viewport.Width() != viewportWidth {
		// Mark content as dirty if width has changed.
		p.viewportDirty = true
		renderedContent = p.renderContent(viewportWidth)
	}

	var content string
	var scrollbar string
	p.viewport.SetWidth(viewportWidth)
	p.viewport.SetHeight(availableHeight)
	if p.viewportDirty {
		p.viewport.SetContent(renderedContent)
		p.viewportWidth = p.viewport.Width()
		p.viewportDirty = false
	}
	content = p.viewport.View()
	if needsScrollbar {
		scrollbar = common.Scrollbar(t, availableHeight, p.viewport.TotalLineCount(), availableHeight, p.viewport.YOffset())
	}

	// Join content with scrollbar if present.
	if scrollbar != "" {
		content = lipgloss.JoinHorizontal(lipgloss.Top, content, scrollbar)
	}

	patternLine := p.renderPatternInput(contentWidth)
	parts := []string{header}
	if patternLine != "" {
		parts = append(parts, "", patternLine)
	}
	if content != "" {
		parts = append(parts, "", content)
	}
	parts = append(parts, "", buttons, "", helpView)

	innerContent := lipgloss.JoinVertical(lipgloss.Left, parts...)
	DrawCenterCursor(scr, area, dialogStyle.Render(innerContent), nil)
	return nil
}

func (p *Permissions) renderHeader(contentWidth int) string {
	t := p.com.Styles

	title := common.DialogTitle(t, "Permission Required", contentWidth-t.Dialog.Title.GetHorizontalFrameSize(), t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor)
	title = t.Dialog.Title.Render(title)

	// Tool info.
	toolLine := p.renderToolName(contentWidth)
	pathLine := p.renderKeyValue("Path", fsext.PrettyPath(p.permission.Path), contentWidth)

	lines := []string{title, "", toolLine, pathLine}

	// Add tool-specific header info.
	switch p.permission.ToolName {
	case tools.BashToolName:
		if params, ok := p.permission.Params.(tools.BashPermissionsParams); ok {
			lines = append(lines, p.renderKeyValue("Desc", params.Description, contentWidth))
		}
	case tools.DownloadToolName:
		if params, ok := p.permission.Params.(tools.DownloadPermissionsParams); ok {
			lines = append(lines, p.renderKeyValue("URL", params.URL, contentWidth))
			lines = append(lines, p.renderKeyValue("File", fsext.PrettyPath(params.FilePath), contentWidth))
		}
	case tools.EditToolName, tools.WriteToolName, tools.MultiEditToolName, tools.ViewToolName:
		var filePath string
		switch params := p.permission.Params.(type) {
		case tools.EditPermissionsParams:
			filePath = params.FilePath
		case tools.WritePermissionsParams:
			filePath = params.FilePath
		case tools.MultiEditPermissionsParams:
			filePath = params.FilePath
		case tools.ViewPermissionsParams:
			filePath = params.FilePath
		}
		if filePath != "" {
			lines = append(lines, p.renderKeyValue("File", fsext.PrettyPath(filePath), contentWidth))
		}
	case tools.LSToolName:
		if params, ok := p.permission.Params.(tools.LSPermissionsParams); ok {
			lines = append(lines, p.renderKeyValue("Directory", fsext.PrettyPath(params.Path), contentWidth))
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (p *Permissions) renderKeyValue(key, value string, width int) string {
	t := p.com.Styles
	keyStyle := t.Dialog.Permissions.KeyText
	valueStyle := t.Dialog.Permissions.ValueText

	keyStr := keyStyle.Render(key)
	valueStr := valueStyle.Width(width - lipgloss.Width(keyStr) - 1).Render(" " + value)

	return lipgloss.JoinHorizontal(lipgloss.Left, keyStr, valueStr)
}

func (p *Permissions) renderToolName(width int) string {
	toolName := p.permission.ToolName

	// Check if this is an MCP tool (format: mcp_<mcpname>_<toolname>).
	if strings.HasPrefix(toolName, "mcp_") {
		parts := strings.SplitN(toolName, "_", 3)
		if len(parts) == 3 {
			mcpName := prettyName(parts[1])
			toolPart := prettyName(parts[2])
			toolName = fmt.Sprintf("%s %s %s", mcpName, styles.ArrowRightIcon, toolPart)
		}
	}

	return p.renderKeyValue("Tool", toolName, width)
}

// prettyName converts snake_case or kebab-case to Title Case.
func prettyName(name string) string {
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	return stringext.Capitalize(name)
}

func (p *Permissions) renderContent(width int) string {
	switch p.permission.ToolName {
	case tools.BashToolName:
		return p.renderBashContent(width)
	case tools.EditToolName:
		return p.renderEditContent(width)
	case tools.WriteToolName:
		return p.renderWriteContent(width)
	case tools.MultiEditToolName:
		return p.renderMultiEditContent(width)
	case tools.DownloadToolName:
		return p.renderDownloadContent(width)
	case tools.FetchToolName:
		return p.renderFetchContent(width)
	case tools.AgenticFetchToolName:
		return p.renderAgenticFetchContent(width)
	case tools.ViewToolName:
		return p.renderViewContent(width)
	case tools.LSToolName:
		return p.renderLSContent(width)
	default:
		return p.renderDefaultContent(width)
	}
}

func (p *Permissions) renderBashContent(width int) string {
	params, ok := p.permission.Params.(tools.BashPermissionsParams)
	if !ok {
		return ""
	}

	return p.renderContentPanel(params.Command, width)
}

func (p *Permissions) renderEditContent(contentWidth int) string {
	params, ok := p.permission.Params.(tools.EditPermissionsParams)
	if !ok {
		return ""
	}
	return p.renderDiff(params.FilePath, params.OldContent, params.NewContent, contentWidth)
}

func (p *Permissions) renderWriteContent(contentWidth int) string {
	params, ok := p.permission.Params.(tools.WritePermissionsParams)
	if !ok {
		return ""
	}
	return p.renderDiff(params.FilePath, params.OldContent, params.NewContent, contentWidth)
}

func (p *Permissions) renderMultiEditContent(contentWidth int) string {
	params, ok := p.permission.Params.(tools.MultiEditPermissionsParams)
	if !ok {
		return ""
	}
	return p.renderDiff(params.FilePath, params.OldContent, params.NewContent, contentWidth)
}

func (p *Permissions) renderDiff(filePath, oldContent, newContent string, contentWidth int) string {
	if !p.viewportDirty {
		if p.isSplitMode() {
			return p.splitDiffContent
		}
		return p.unifiedDiffContent
	}

	isSplitMode := p.isSplitMode()
	formatter := common.DiffFormatter(p.com.Styles).
		Before(fsext.PrettyPath(filePath), oldContent).
		After(fsext.PrettyPath(filePath), newContent).
		XOffset(p.diffXOffset).
		Width(contentWidth)

	var result string
	if isSplitMode {
		formatter = formatter.Split()
		p.splitDiffContent = formatter.String()
		result = p.splitDiffContent
	} else {
		formatter = formatter.Unified()
		p.unifiedDiffContent = formatter.String()
		result = p.unifiedDiffContent
	}

	return result
}

func (p *Permissions) renderDownloadContent(width int) string {
	params, ok := p.permission.Params.(tools.DownloadPermissionsParams)
	if !ok {
		return ""
	}

	content := fmt.Sprintf("URL: %s\nFile: %s", params.URL, fsext.PrettyPath(params.FilePath))
	if params.Timeout > 0 {
		content += fmt.Sprintf("\nTimeout: %ds", params.Timeout)
	}

	return p.renderContentPanel(content, width)
}

func (p *Permissions) renderFetchContent(width int) string {
	params, ok := p.permission.Params.(tools.FetchPermissionsParams)
	if !ok {
		return ""
	}

	return p.renderContentPanel(params.URL, width)
}

func (p *Permissions) renderAgenticFetchContent(width int) string {
	params, ok := p.permission.Params.(tools.AgenticFetchPermissionsParams)
	if !ok {
		return ""
	}

	var content string
	if params.URL != "" {
		content = fmt.Sprintf("URL: %s\n\nPrompt: %s", params.URL, params.Prompt)
	} else {
		content = fmt.Sprintf("Prompt: %s", params.Prompt)
	}

	return p.renderContentPanel(content, width)
}

func (p *Permissions) renderViewContent(width int) string {
	params, ok := p.permission.Params.(tools.ViewPermissionsParams)
	if !ok {
		return ""
	}

	content := fmt.Sprintf("File: %s", fsext.PrettyPath(params.FilePath))
	if params.Offset > 0 {
		content += fmt.Sprintf("\nStarting from line: %d", params.Offset+1)
	}
	if params.Limit > 0 && params.Limit != 2000 {
		content += fmt.Sprintf("\nLines to read: %d", params.Limit)
	}

	return p.renderContentPanel(content, width)
}

func (p *Permissions) renderLSContent(width int) string {
	params, ok := p.permission.Params.(tools.LSPermissionsParams)
	if !ok {
		return ""
	}

	content := fmt.Sprintf("Directory: %s", fsext.PrettyPath(params.Path))
	if len(params.Ignore) > 0 {
		content += fmt.Sprintf("\nIgnore patterns: %s", strings.Join(params.Ignore, ", "))
	}

	return p.renderContentPanel(content, width)
}

func (p *Permissions) renderDefaultContent(width int) string {
	t := p.com.Styles
	var content string
	// do not add the description for mcp tools
	if !strings.HasPrefix(p.permission.ToolName, "mcp_") {
		content = p.permission.Description
	}

	// Pretty-print JSON params if available.
	if p.permission.Params != nil {
		var paramStr string
		if str, ok := p.permission.Params.(string); ok {
			paramStr = str
		} else {
			paramStr = fmt.Sprintf("%v", p.permission.Params)
		}

		var parsed any
		if err := json.Unmarshal([]byte(paramStr), &parsed); err == nil {
			if b, err := json.MarshalIndent(parsed, "", "  "); err == nil {
				jsonContent := string(b)
				highlighted, err := common.SyntaxHighlight(t, jsonContent, "params.json", t.Dialog.Permissions.ParamsBg)
				if err == nil {
					jsonContent = highlighted
				}
				if content != "" {
					content += "\n\n"
				}
				content += jsonContent
			}
		} else if paramStr != "" {
			if content != "" {
				content += "\n\n"
			}
			content += paramStr
		}
	}

	if content == "" {
		return ""
	}

	return p.renderContentPanel(strings.TrimSpace(content), width)
}

// renderContentPanel renders content in a panel with the full width.
func (p *Permissions) renderContentPanel(content string, width int) string {
	panelStyle := p.com.Styles.Dialog.ContentPanel
	return panelStyle.Width(width).Render(content)
}

// renderPatternInput renders the editable pattern field.
func (p *Permissions) renderPatternInput(width int) string {
	// Only show pattern field if there's an input to edit.
	if p.permission.Input == "" {
		return ""
	}
	t := p.com.Styles
	label := t.Dialog.Permissions.KeyText.Render("Pattern")

	p.patternInput.SetWidth(width - lipgloss.Width(label) - 1)
	inputView := p.patternInput.View()

	return lipgloss.JoinHorizontal(lipgloss.Left, label, " ", inputView)
}

func (p *Permissions) renderButtons(contentWidth int) string {
	if p.denyReasonVisible {
		return p.renderDenyReasonInput(contentWidth)
	}

	var buttons []common.ButtonOpts
	if p.foreverExpanded {
		buttons = []common.ButtonOpts{
			{Text: "Project", UnderlineIndex: 0, Selected: p.selectedOption == 0},
			{Text: "User", UnderlineIndex: 0, Selected: p.selectedOption == 1},
		}
	} else {
		buttons = []common.ButtonOpts{
			{Text: "Allow", UnderlineIndex: 0, Selected: p.selectedOption == 0},
			{Text: "Session", UnderlineIndex: 0, Selected: p.selectedOption == 1},
			{Text: "Forever", UnderlineIndex: 0, Selected: p.selectedOption == 2},
			{Text: "Deny", UnderlineIndex: 0, Selected: p.selectedOption == 3},
		}
	}

	content := common.ButtonGroup(p.com.Styles, buttons, "  ")

	// If buttons are too wide, stack them vertically.
	if lipgloss.Width(content) > contentWidth {
		content = common.ButtonGroup(p.com.Styles, buttons, "\n")
		return lipgloss.NewStyle().
			Width(contentWidth).
			Align(lipgloss.Center).
			Render(content)
	}

	return lipgloss.NewStyle().
		Width(contentWidth).
		Align(lipgloss.Right).
		Render(content)
}

// renderDenyReasonInput renders the deny reason text input prompt.
func (p *Permissions) renderDenyReasonInput(contentWidth int) string {
	t := p.com.Styles
	label := t.Dialog.Permissions.KeyText.Render("Reason:")
	hint := t.Dialog.Permissions.ValueText.Render("  (Enter to confirm, Escape to skip)")

	// Render the input field with an underline cursor.
	inputWidth := contentWidth - lipgloss.Width(label) - lipgloss.Width(hint) - 2
	if inputWidth < 10 {
		inputWidth = 10
	}
	inputText := p.denyReasonInput
	if len([]rune(inputText)) > inputWidth {
		runes := []rune(inputText)
		inputText = string(runes[len(runes)-inputWidth:])
	}

	// Pad to fill the input field width.
	padding := inputWidth - len([]rune(inputText))
	if padding < 0 {
		padding = 0
	}
	cursor := inputText + strings.Repeat("_", padding)

	inputStyle := t.Dialog.Permissions.ValueText
	content := lipgloss.JoinHorizontal(lipgloss.Left, label, " ", inputStyle.Render(cursor), hint)

	return lipgloss.NewStyle().
		Width(contentWidth).
		Align(lipgloss.Left).
		Render(content)
}

func (p *Permissions) canScroll() bool {
	if p.hasDiffView() {
		// Diff views can always scroll.
		return true
	}
	// For non-diff content, check if viewport has scrollable content.
	return !p.viewport.AtTop() || !p.viewport.AtBottom()
}

// ShortHelp implements [help.KeyMap].
func (p *Permissions) ShortHelp() []key.Binding {
	bindings := []key.Binding{
		p.keyMap.Choose,
		p.keyMap.Select,
		p.keyMap.Close,
	}

	if p.permission.Input != "" {
		bindings = append(bindings, p.keyMap.EditPattern)
	}

	if p.canScroll() {
		bindings = append(bindings, p.keyMap.Scroll)
	}

	if p.hasDiffView() {
		bindings = append(bindings,
			p.keyMap.ToggleDiffMode,
			p.keyMap.ToggleFullscreen,
		)
	}

	return bindings
}

// FullHelp implements [help.KeyMap].
func (p *Permissions) FullHelp() [][]key.Binding {
	return [][]key.Binding{p.ShortHelp()}
}

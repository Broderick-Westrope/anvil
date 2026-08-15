package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/charmbracelet/x/term"
	"github.com/dustin/go-humanize"
	"github.com/sahilm/fuzzy"
	"github.com/spf13/cobra"
)

var sessionPinnedCmd = &cobra.Command{
	Use:   "pinned",
	Short: "Browse and resume pinned sessions",
	Long:  "Browse pinned sessions across all projects in an interactive picker. Enter resumes the selected session in its original working directory. Falls back to `session list --pinned` output when not attached to a terminal.",
	RunE:  runSessionPinned,
}

func init() {
	sessionCmd.AddCommand(sessionPinnedCmd)
}

func runSessionPinned(cmd *cobra.Command, _ []string) error {
	// Non-TTY (either side): fall back to the plain list output.
	if !term.IsTerminal(os.Stdout.Fd()) || !term.IsTerminal(os.Stdin.Fd()) {
		sessionListPinned = true
		return runSessionList(cmd, nil)
	}

	ctx, svc, cleanup, err := sessionSetup(cmd)
	if err != nil {
		return err
	}

	sessions, err := svc.sessions.ListPinned(ctx)
	if err != nil {
		cleanup()
		return fmt.Errorf("failed to list pinned sessions: %w", err)
	}
	if len(sessions) == 0 {
		cleanup()
		fmt.Fprintln(cmd.OutOrStdout(), "No pinned sessions.")
		return nil
	}

	m := newPickerModel(ctx, svc, sessions)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	finalModel, err := p.Run()
	if err != nil {
		cleanup()
		return err
	}

	resumeID := ""
	if fm, ok := finalModel.(*pickerModel); ok {
		resumeID = fm.resumeID
	}
	if resumeID != "" {
		// Close the DB connection explicitly: syscall.Exec replaces the
		// process and would skip deferred cleanup.
		cleanup()
		return execResume(resumeID)
	}
	cleanup()
	return nil
}

// pickerStyles holds the lipgloss styles used by the picker.
type pickerStyles struct {
	item        lipgloss.Style
	itemFocused lipgloss.Style
	dim         lipgloss.Style
	dimFocused  lipgloss.Style
	missing     lipgloss.Style
	status      lipgloss.Style
	errStatus   lipgloss.Style
	help        lipgloss.Style
	prompt      lipgloss.Style
}

func newPickerStyles() pickerStyles {
	return pickerStyles{
		item:        lipgloss.NewStyle(),
		itemFocused: lipgloss.NewStyle().Background(charmtone.Charple).Foreground(charmtone.Butter),
		dim:         lipgloss.NewStyle().Foreground(charmtone.Squid),
		dimFocused:  lipgloss.NewStyle().Background(charmtone.Charple).Foreground(charmtone.Smoke),
		missing:     lipgloss.NewStyle().Foreground(charmtone.Coral),
		status:      lipgloss.NewStyle().Foreground(charmtone.Zest),
		errStatus:   lipgloss.NewStyle().Foreground(charmtone.Coral),
		help:        lipgloss.NewStyle().Foreground(charmtone.Squid),
		prompt:      lipgloss.NewStyle().Foreground(charmtone.Malibu),
	}
}

// pickerItem wraps a pinned session for display in the picker list.
type pickerItem struct {
	*list.Versioned
	sess    session.Session
	styles  pickerStyles
	m       fuzzy.Match
	focused bool
	missing bool
}

var _ list.FilterableItem = &pickerItem{}

func newPickerItem(sess session.Session, styles pickerStyles) *pickerItem {
	missing := false
	if _, err := os.Stat(sess.WorkingDir); os.IsNotExist(err) {
		missing = true
	}
	return &pickerItem{
		Versioned: list.NewVersioned(),
		sess:      sess,
		styles:    styles,
		missing:   missing,
	}
}

// Filter returns the searchable value: title and pin note.
func (p *pickerItem) Filter() string {
	return p.sess.Title + " " + p.sess.PinNote
}

// Finished implements list.Item; picker items are render-stable outside
// of SetFocused / SetMatch, both of which bump the version.
func (p *pickerItem) Finished() bool {
	return true
}

// SetFocused implements list.Focusable.
func (p *pickerItem) SetFocused(focused bool) {
	if p.focused == focused {
		return
	}
	p.focused = focused
	p.Bump()
}

// SetMatch implements list.MatchSettable.
func (p *pickerItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatchCmd(p.m, m) {
		return
	}
	p.m = m
	p.Bump()
}

// sameFuzzyMatchCmd reports whether two fuzzy.Match values are observably
// equal; fuzzy.Match contains a slice so it is not comparable with ==.
func sameFuzzyMatchCmd(a, b fuzzy.Match) bool {
	if a.Str != b.Str || a.Index != b.Index || a.Score != b.Score {
		return false
	}
	if len(a.MatchedIndexes) != len(b.MatchedIndexes) {
		return false
	}
	for i := range a.MatchedIndexes {
		if a.MatchedIndexes[i] != b.MatchedIndexes[i] {
			return false
		}
	}
	return true
}

// Render renders a single picker row: title, dim note, dim abbreviated
// workdir, and age. Missing working dirs get a styled marker.
func (p *pickerItem) Render(width int) string {
	itemStyle := p.styles.item
	dimStyle := p.styles.dim
	if p.focused {
		itemStyle = p.styles.itemFocused
		dimStyle = p.styles.dimFocused
	}

	age := humanize.Time(time.Unix(p.sess.UpdatedAt, 0))
	workdir := ansi.Truncate(abbreviateHome(p.sess.WorkingDir), 32, "…")
	right := workdir + "  " + age
	if p.missing {
		right = workdir + " " + p.styles.missing.Render("(missing)") + "  " + age
	}
	rightWidth := lipgloss.Width(right)

	leftWidth := max(width-rightWidth-2, 10)
	title := strings.ReplaceAll(p.sess.Title, "\n", " ")
	var left string
	if p.sess.PinNote != "" {
		note := strings.ReplaceAll(p.sess.PinNote, "\n", " ")
		titleWidth := max(leftWidth/2, 10)
		title = ansi.Truncate(title, titleWidth, "…")
		note = ansi.Truncate(note, max(leftWidth-lipgloss.Width(title)-3, 0), "…")
		left = itemStyle.Render(title) + dimStyle.Render(" — "+note)
	} else {
		left = itemStyle.Render(ansi.Truncate(title, leftWidth, "…"))
	}

	pad := max(width-lipgloss.Width(left)-rightWidth, 1)
	return left + strings.Repeat(" ", pad) + dimStyle.Render(right)
}

// pickerModel is the Bubble Tea model for the pinned session picker.
type pickerModel struct {
	ctx    context.Context
	svc    *sessionServices
	styles pickerStyles

	list  *list.FilterableList
	input textinput.Model

	width, height int

	// confirmUnpin is set while awaiting y/n confirmation for ctrl+x.
	confirmUnpin bool
	// status holds an inline status/error line; cleared on movement.
	status    string
	statusErr bool

	// resumeID holds the UUID of the session selected for resume; the
	// exec handoff happens after Run() returns, never inside the program.
	resumeID string
}

func newPickerModel(ctx context.Context, svc *sessionServices, sessions []session.Session) *pickerModel {
	styles := newPickerStyles()
	items := make([]list.FilterableItem, len(sessions))
	for i, s := range sessions {
		items[i] = newPickerItem(s, styles)
	}
	l := list.NewFilterableList(items...)
	l.Focus()
	l.SetSelected(0)

	input := textinput.New()
	input.SetVirtualCursor(true)
	input.Placeholder = "Filter pinned sessions"
	input.Focus()

	return &pickerModel{
		ctx:    ctx,
		svc:    svc,
		styles: styles,
		list:   l,
		input:  input,
	}
}

// Init implements tea.Model.
func (m *pickerModel) Init() tea.Cmd {
	return nil
}

// unpinResultMsg carries the result of an unpin write.
type unpinResultMsg struct {
	id  string
	err error
}

func (m *pickerModel) unpinCmd(id string) tea.Cmd {
	return func() tea.Msg {
		return unpinResultMsg{id: id, err: m.svc.sessions.SetPin(m.ctx, id, false, "")}
	}
}

// selectedItem returns the currently highlighted picker item, or nil.
func (m *pickerModel) selectedItem() *pickerItem {
	item := m.list.SelectedItem()
	if item == nil {
		return nil
	}
	pi, ok := item.(*pickerItem)
	if !ok {
		return nil
	}
	return pi
}

// removeSession drops a session from the underlying item set and
// re-applies the current filter.
func (m *pickerModel) removeSession(id string) {
	items := m.list.Items()
	kept := make([]list.FilterableItem, 0, len(items))
	for _, it := range items {
		if pi, ok := it.(*pickerItem); ok && pi.sess.ID == id {
			continue
		}
		kept = append(kept, it)
	}
	m.list.SetItems(kept...)
	m.list.SetFilter(m.input.Value())
	m.list.SetSelected(0)
}

// Update implements tea.Model.
func (m *pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil

	case unpinResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("failed to unpin: %v", msg.err)
			m.statusErr = true
			return m, nil
		}
		m.removeSession(msg.id)
		if m.list.Len() == 0 {
			return m, tea.Quit
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *pickerModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Unpin confirmation intercepts everything until answered.
	if m.confirmUnpin {
		switch msg.String() {
		case "y":
			m.confirmUnpin = false
			m.status = ""
			if pi := m.selectedItem(); pi != nil {
				return m, m.unpinCmd(pi.sess.ID)
			}
			return m, nil
		case "n", "esc", "ctrl+c":
			m.confirmUnpin = false
			m.status = ""
			return m, nil
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c", "esc":
		return m, tea.Quit

	case "up", "ctrl+p":
		if m.list.IsSelectedFirst() {
			m.list.SelectLast()
		} else {
			m.list.SelectPrev()
		}
		m.list.ScrollToSelected()
		m.status = ""
		return m, nil

	case "down", "ctrl+n":
		if m.list.IsSelectedLast() {
			m.list.SelectFirst()
		} else {
			m.list.SelectNext()
		}
		m.list.ScrollToSelected()
		m.status = ""
		return m, nil

	case "enter":
		pi := m.selectedItem()
		if pi == nil {
			return m, nil
		}
		if pi.missing {
			m.status = fmt.Sprintf(
				"working directory no longer exists — resume with: anvil --session %s --cwd <dir>",
				session.HashID(pi.sess.ID)[:7],
			)
			m.statusErr = true
			return m, nil
		}
		m.resumeID = pi.sess.ID
		return m, tea.Quit

	case "ctrl+x":
		pi := m.selectedItem()
		if pi == nil {
			return m, nil
		}
		title := ansi.Truncate(strings.ReplaceAll(pi.sess.Title, "\n", " "), 40, "…")
		m.confirmUnpin = true
		m.status = fmt.Sprintf("unpin %q? (y/n)", title)
		m.statusErr = false
		return m, nil
	}

	// Everything else feeds the filter input.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.list.SetFilter(m.input.Value())
	m.list.SetSelected(0)
	m.list.ScrollToTop()
	m.status = ""
	return m, cmd
}

// chromeHeight is the number of non-list lines in the picker view:
// filter input, status line, and help line.
const pickerChromeHeight = 3

// resize recomputes component sizes from the current terminal size.
func (m *pickerModel) resize() {
	listHeight := max(m.height-pickerChromeHeight, 1)
	m.list.SetSize(m.listWidth(), listHeight)
	m.input.SetWidth(max(m.width-2, 10))
}

// listWidth returns the width available to the list pane.
func (m *pickerModel) listWidth() int {
	return m.width
}

// View implements tea.Model.
func (m *pickerModel) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}

	var b strings.Builder
	b.WriteString(m.styles.prompt.Render("> ") + m.input.View())
	b.WriteString("\n")

	b.WriteString(m.list.Render())
	b.WriteString("\n")

	statusStyle := m.styles.status
	if m.statusErr {
		statusStyle = m.styles.errStatus
	}
	if m.status != "" {
		b.WriteString(statusStyle.Render(ansi.Truncate(m.status, m.width, "…")))
	}
	b.WriteString("\n")
	b.WriteString(m.styles.help.Render("↑/↓ move · enter resume · ctrl+x unpin · esc quit"))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

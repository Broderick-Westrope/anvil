package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
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
		return runSessionListImpl(cmd, true)
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
	if sameFuzzyMatch(p.m, m) {
		return
	}
	p.m = m
	p.Bump()
}

// sameFuzzyMatch reports whether two fuzzy.Match values are observably
// equal; fuzzy.Match contains a slice so it is not comparable with ==.
func sameFuzzyMatch(a, b fuzzy.Match) bool {
	return a.Str == b.Str &&
		a.Index == b.Index &&
		a.Score == b.Score &&
		slices.Equal(a.MatchedIndexes, b.MatchedIndexes)
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

	preview pickerPreview
}

// pickerPreview holds the transcript preview pane state. The pane is off
// by default on every invocation and toggles with tab.
type pickerPreview struct {
	visible    bool
	fullscreen bool
	// seq is a monotonically increasing request sequence; results
	// carrying a stale seq are discarded.
	seq int
	// cache holds loaded transcript state per session ID for the
	// picker's lifetime.
	cache map[string]*previewEntry
}

// previewEntry is the cached transcript state for one session.
type previewEntry struct {
	// msgs are the loaded messages, oldest-first.
	msgs []message.Message
	// lines are the rendered tail lines at width.
	lines []string
	width int
	// offset is the index of the first visible line in the viewport.
	offset int
	// oldestParent is the ParentMessageID of the oldest loaded message;
	// empty means there is no more history to fetch.
	oldestParent string
	// seq is the request sequence of the latest load issued for this
	// entry; results with an older seq are discarded as stale.
	seq       int
	loading   bool
	exhausted bool
	loadErr   error
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
		preview: pickerPreview{
			cache: make(map[string]*previewEntry),
		},
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
// re-applies the current filter, keeping the cursor near its previous
// position.
func (m *pickerModel) removeSession(id string) {
	previous := m.list.Selected()
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
	m.list.SetSelected(max(min(previous, m.list.Len()-1), 0))
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

	case previewLoadedMsg:
		m.handlePreviewLoaded(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// previewLoadedMsg carries an async transcript page load result.
type previewLoadedMsg struct {
	seq       int
	sessionID string
	msgs      []message.Message
	prepend   bool
	err       error
}

// previewLoadCmd fires an async transcript load for the highlighted
// session when the preview is showing and the session isn't cached yet.
func (m *pickerModel) previewLoadCmd() tea.Cmd {
	if !m.preview.visible && !m.preview.fullscreen {
		return nil
	}
	pi := m.selectedItem()
	if pi == nil {
		return nil
	}
	if _, ok := m.preview.cache[pi.sess.ID]; ok {
		return nil
	}

	leaf := strings.TrimSpace(pi.sess.LeafMessageID)
	if leaf == "" {
		// Empty session: cache the placeholder synchronously.
		m.preview.cache[pi.sess.ID] = &previewEntry{exhausted: true}
		return nil
	}

	m.preview.seq++
	seq := m.preview.seq
	m.preview.cache[pi.sess.ID] = &previewEntry{loading: true, seq: seq}
	sessionID := pi.sess.ID
	return func() tea.Msg {
		msgs, err := m.svc.messages.GetBranchPathTail(m.ctx, leaf, pickerPageSize)
		return previewLoadedMsg{seq: seq, sessionID: sessionID, msgs: msgs, err: err}
	}
}

// previewFetchOlderCmd fetches the previous transcript page when the
// viewport is scrolled to the top of the loaded history.
func (m *pickerModel) previewFetchOlderCmd() tea.Cmd {
	entry, sessionID := m.currentPreviewEntry()
	if entry == nil || entry.loading || entry.exhausted || entry.offset > 0 {
		return nil
	}
	if entry.oldestParent == "" {
		entry.exhausted = true
		return nil
	}
	entry.loading = true
	m.preview.seq++
	seq := m.preview.seq
	entry.seq = seq
	leaf := entry.oldestParent
	return func() tea.Msg {
		msgs, err := m.svc.messages.GetBranchPathTail(m.ctx, leaf, pickerPageSize)
		return previewLoadedMsg{seq: seq, sessionID: sessionID, msgs: msgs, prepend: true, err: err}
	}
}

// handlePreviewLoaded applies an async load result, discarding stale
// responses (the entry has a newer request seq).
func (m *pickerModel) handlePreviewLoaded(msg previewLoadedMsg) {
	entry, ok := m.preview.cache[msg.sessionID]
	if !ok || msg.seq != entry.seq {
		return
	}
	entry.loading = false
	if msg.err != nil {
		entry.loadErr = msg.err
		return
	}
	entry.loadErr = nil

	if msg.prepend {
		if len(msg.msgs) == 0 {
			entry.exhausted = true
			return
		}
		prevLines := len(entry.lines)
		entry.msgs = append(append([]message.Message{}, msg.msgs...), entry.msgs...)
		entry.oldestParent = msg.msgs[0].ParentMessageID
		if entry.oldestParent == "" {
			entry.exhausted = true
		}
		entry.renderAt(entry.width)
		// Preserve the scroll position across the prepend.
		entry.offset += len(entry.lines) - prevLines
		return
	}

	entry.msgs = msg.msgs
	if len(msg.msgs) > 0 {
		entry.oldestParent = msg.msgs[0].ParentMessageID
		entry.exhausted = entry.oldestParent == ""
	} else {
		entry.exhausted = true
	}
	entry.renderAt(entry.width)
	// Start at the bottom (latest messages).
	entry.offset = -1
}

// renderAt (re-)renders the entry's tail lines at the given width.
func (e *previewEntry) renderAt(width int) {
	e.width = max(width, 10)
	e.lines = renderTail(e.msgs, e.width)
}

// currentPreviewEntry returns the cache entry for the highlighted session.
func (m *pickerModel) currentPreviewEntry() (*previewEntry, string) {
	pi := m.selectedItem()
	if pi == nil {
		return nil, ""
	}
	return m.preview.cache[pi.sess.ID], pi.sess.ID
}

// pickerSplitMinWidth is the minimum terminal width for the split
// preview layout; narrower terminals get a full-width preview.
const pickerSplitMinWidth = 100

// togglePreview flips the preview pane. Narrow terminals get a
// full-width preview instead of a split.
func (m *pickerModel) togglePreview() tea.Cmd {
	if m.preview.visible || m.preview.fullscreen {
		m.preview.visible = false
		m.preview.fullscreen = false
		m.resize()
		return nil
	}
	if m.width < pickerSplitMinWidth {
		m.preview.fullscreen = true
	} else {
		m.preview.visible = true
	}
	m.resize()
	return m.previewLoadCmd()
}

// previewPageStride is the number of lines a pgup/pgdown scroll moves.
func (m *pickerModel) previewPageStride() int {
	return max(m.previewBodyHeight()/2, 1)
}

// previewBodyHeight returns the transcript viewport height inside the pane.
func (m *pickerModel) previewBodyHeight() int {
	paneHeight := max(m.height-pickerChromeHeight, 1)
	if m.preview.fullscreen {
		paneHeight = max(m.height, 1)
	}
	pi := m.selectedItem()
	if pi == nil {
		return paneHeight
	}
	headerLines := len(renderPreviewHeader(pi.sess, m.previewWidth())) + 1
	return max(paneHeight-headerLines, 1)
}

// previewWidth returns the inner width of the preview pane.
func (m *pickerModel) previewWidth() int {
	if m.preview.fullscreen {
		return max(m.width, 10)
	}
	return max(m.width-m.listWidth()-1, 10)
}

// previewScroll moves the transcript viewport by delta lines.
func (m *pickerModel) previewScroll(delta int) {
	if !m.preview.visible && !m.preview.fullscreen {
		return
	}
	entry, _ := m.currentPreviewEntry()
	if entry == nil {
		return
	}
	bodyHeight := m.previewBodyHeight()
	maxOffset := max(len(entry.lines)-bodyHeight, 0)
	if entry.offset < 0 {
		entry.offset = maxOffset
	}
	entry.offset = min(max(entry.offset+delta, 0), maxOffset)
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

	// Full-width preview mode (narrow terminals): scroll keys page the
	// transcript; any other key returns to the list.
	if m.preview.fullscreen {
		switch msg.String() {
		case "pgup", "ctrl+u":
			m.previewScroll(-m.previewPageStride())
			return m, m.previewFetchOlderCmd()
		case "pgdown", "ctrl+d":
			m.previewScroll(m.previewPageStride())
			return m, nil
		}
		m.preview.fullscreen = false
		m.resize()
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
		return m, m.previewLoadCmd()

	case "down", "ctrl+n":
		if m.list.IsSelectedLast() {
			m.list.SelectFirst()
		} else {
			m.list.SelectNext()
		}
		m.list.ScrollToSelected()
		m.status = ""
		return m, m.previewLoadCmd()

	case "tab":
		return m, m.togglePreview()

	case "pgup", "ctrl+u":
		if !m.preview.visible && !m.preview.fullscreen {
			return m, nil
		}
		m.previewScroll(-m.previewPageStride())
		return m, m.previewFetchOlderCmd()

	case "pgdown", "ctrl+d":
		if !m.preview.visible && !m.preview.fullscreen {
			return m, nil
		}
		m.previewScroll(m.previewPageStride())
		return m, nil

	case "enter":
		pi := m.selectedItem()
		if pi == nil {
			return m, nil
		}
		if pi.missing {
			m.status = fmt.Sprintf(
				"working directory no longer exists — resume with: anvil --session %s --cwd <dir>",
				session.HashID(pi.sess.ID),
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
	return m, tea.Batch(cmd, m.previewLoadCmd())
}

// pickerChromeHeight is the number of non-list lines in the picker view:
// filter input, status line, and help line.
const pickerChromeHeight = 3

// resize recomputes component sizes from the current terminal size.
func (m *pickerModel) resize() {
	// Re-evaluate the preview layout: a live resize can cross the
	// split-width threshold while the preview is open.
	if m.preview.visible && m.width < pickerSplitMinWidth {
		m.preview.visible = false
		m.preview.fullscreen = true
	} else if m.preview.fullscreen && m.width >= pickerSplitMinWidth {
		m.preview.fullscreen = false
		m.preview.visible = true
	}
	listHeight := max(m.height-pickerChromeHeight, 1)
	m.list.SetSize(m.listWidth(), listHeight)
	m.input.SetWidth(max(m.width-2, 10))
}

// listWidth returns the width available to the list pane.
func (m *pickerModel) listWidth() int {
	if m.preview.visible {
		return m.width * 45 / 100
	}
	return m.width
}

// renderPreviewPane renders the metadata header, separator, and the
// visible transcript viewport slice for the highlighted session.
func (m *pickerModel) renderPreviewPane(width, height int) string {
	width = max(width, 10)
	pi := m.selectedItem()
	if pi == nil {
		return m.styles.dim.Render("no selection")
	}

	header := renderPreviewHeader(pi.sess, width)
	lines := make([]string, 0, height)
	for _, h := range header {
		lines = append(lines, m.styles.dim.Render(h))
	}
	lines = append(lines, m.styles.dim.Render(strings.Repeat("─", width)))

	bodyHeight := max(height-len(lines), 1)
	entry := m.preview.cache[pi.sess.ID]
	switch {
	case entry == nil || (entry.loading && len(entry.lines) == 0):
		lines = append(lines, m.styles.dim.Render("loading…"))
	case entry.loadErr != nil:
		lines = append(lines, m.styles.errStatus.Render(ansi.Truncate("error: "+entry.loadErr.Error(), width, "…")))
	default:
		if entry.width != width || len(entry.lines) == 0 {
			entry.renderAt(width)
		}
		body := entry.lines
		maxOffset := max(len(body)-bodyHeight, 0)
		offset := entry.offset
		if offset < 0 || offset > maxOffset {
			offset = maxOffset
		}
		for _, line := range body[offset:min(offset+bodyHeight, len(body))] {
			if line == string(message.User) || line == string(message.Assistant) {
				lines = append(lines, m.styles.dim.Render(line))
				continue
			}
			lines = append(lines, ansi.Truncate(line, width, "…"))
		}
	}

	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// View implements tea.Model.
func (m *pickerModel) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}

	if m.preview.fullscreen {
		v := tea.NewView(m.renderPreviewPane(m.width, max(m.height, 1)))
		v.AltScreen = true
		return v
	}

	var b strings.Builder
	b.WriteString(m.styles.prompt.Render("> ") + m.input.View())
	b.WriteString("\n")

	body := m.list.Render()
	if m.preview.visible {
		pane := m.renderPreviewPane(m.previewWidth(), max(m.height-pickerChromeHeight, 1))
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(m.listWidth()).Render(body),
			" ",
			pane,
		)
	}
	b.WriteString(body)
	b.WriteString("\n")

	statusStyle := m.styles.status
	if m.statusErr {
		statusStyle = m.styles.errStatus
	}
	if m.status != "" {
		b.WriteString(statusStyle.Render(ansi.Truncate(m.status, m.width, "…")))
	}
	b.WriteString("\n")
	b.WriteString(m.styles.help.Render("↑/↓ move · enter resume · tab preview · pgup/pgdn scroll · ctrl+x unpin · esc quit"))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

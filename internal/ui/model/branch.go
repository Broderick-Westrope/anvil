package model

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
)

// branchBannerLine1 and branchBannerLine2 are the two fixed lines shown
// above the composer while a branch preview or submission is active.
const (
	branchBannerLine2 = "Enter send · Esc cancel"
)

// composerSnapshot captures everything needed to restore the composer
// exactly as the user left it: raw text, file/image attachments, and
// attached skill pills.
type composerSnapshot struct {
	text        string
	attachments []message.Attachment
	skills      []attachments.SkillAttachment
}

// isEmpty reports whether the snapshot represents a fully blank composer.
func (c composerSnapshot) isEmpty() bool {
	return c.text == "" && len(c.attachments) == 0 && len(c.skills) == 0
}

// historySnapshot captures prompt-history navigation state.
type historySnapshot struct {
	messages []string
	index    int
	draft    string
}

// branchPreview holds the read-only preview state installed when the user
// presses Shift+B on an eligible message. No persistent mutation (leaf
// movement, DB write) happens until the previewed draft is submitted.
type branchPreview struct {
	targetID       string
	targetRole     message.MessageRole
	sessionID      string
	expectedLeafID string
	shortID        string

	originalDraft   composerSnapshot
	originalHistory historySnapshot
	originalFocus   uiFocusState
}

// branchRun tracks an in-flight branch submission from Enter until its
// persisted state has been fully reconciled. Populated by trySubmitBranch.
// All fields are only ever touched from Update, never from a tea.Cmd
// goroutine, except through messages compared by run pointer identity.
type branchRun struct {
	sessionID string
	// outcome carries exactly one accepted (optional) and one finished
	// item from the run's own goroutine; capacity 2 so both sends
	// complete even if this run is superseded before either is read.
	outcome chan branchOutcome
	// preview is the original preview, retained so a pre-acceptance
	// failure can revert to an editable preview with the payload intact.
	preview *branchPreview

	accepted       bool
	acceptedUserID string
	finished       bool
	finishErr      error

	// reconciledAfterFinish is set once a read succeeds after finished,
	// satisfying the mandatory final-snapshot requirement. branchActive
	// stays true until then so new mutations/previews remain blocked.
	reconciledAfterFinish bool

	// dirty/reading drive the single serialized read loop: at most one
	// read is ever in flight per run, and events observed during that
	// read are folded into dirty for the next one.
	dirty     bool
	reading   bool
	failCount int
}

// branchOutcomeKind distinguishes the two items ever sent on a
// branchRun's outcome channel.
type branchOutcomeKind int

const (
	branchOutcomeAccepted branchOutcomeKind = iota
	branchOutcomeFinished
)

// branchOutcome is one item sent on a branchRun's outcome channel,
// independent of pubsub.
type branchOutcome struct {
	kind   branchOutcomeKind
	userID string // set for branchOutcomeAccepted
	err    error  // set for branchOutcomeFinished
}

// branchOutcomeMsg wraps a received branchOutcome together with the run
// it belongs to, so stale messages from a superseded/torn-down run can be
// detected by pointer identity and ignored.
type branchOutcomeMsg struct {
	run     *branchRun
	outcome branchOutcome
}

// branchReadResultMsg is the result of one serialized GetSession +
// GetBranchPath reconciliation read.
type branchReadResultMsg struct {
	run      *branchRun
	session  *session.Session
	messages []message.Message
	err      error
}

// branchPollMsg drives the periodic reconciliation loop: it both repairs
// dropped pubsub events (by forcing dirty) and retries failed reads,
// running for the lifetime of its run (including after finished, so an
// already-queued follow-up turn still converges even if all of its
// events are dropped).
type branchPollMsg struct {
	run *branchRun
}

// branchPollInterval is how often the reconciliation loop wakes up to
// check for dirtiness (from pubsub) or force one anyway (watchdog).
const branchPollInterval = 200 * time.Millisecond

// branchReturnSnapshot is the single recoverable pre-branch snapshot kept
// after a branch is accepted, allowing "Return to pre-branch conversation".
type branchReturnSnapshot struct {
	sessionID       string
	leafID          string
	originalDraft   composerSnapshot
	originalHistory historySnapshot
	originalFocus   uiFocusState
}

// branchReturnResultMsg is the result of the "Return to pre-branch
// conversation" command.
type branchReturnResultMsg struct {
	session   *session.Session
	messages  []message.Message
	err       error
	movedLeaf bool // true if MoveLeaf succeeded but a later read failed
}

// mutationBranchReturn identifies an in-flight "Return to pre-branch
// conversation" as a tracked mutation.
const mutationBranchReturn = "branch-return"

// branchActive reports whether a branch preview or an in-flight branch
// submission currently owns the composer. Once a submitted branch has
// finished AND its mandatory post-finish snapshot has been read, this
// returns false again even though m.branchRun stays non-nil to keep the
// reconciliation poll loop alive for already-queued follow-up work.
func (m *UI) branchActive() bool {
	if m.branchPreview != nil {
		return true
	}
	if m.branchRun == nil {
		return false
	}
	return !(m.branchRun.finished && m.branchRun.reconciledAfterFinish)
}

// beginMutation registers a pending mutation under id, blocking new branch
// previews/submissions until endMutation(id) is called. IDs are unique per
// operation kind; registering the same id twice is a no-op (single
// in-flight operation of that kind at a time).
func (m *UI) beginMutation(id string) {
	if m.pendingMutations == nil {
		m.pendingMutations = make(map[string]struct{})
	}
	m.pendingMutations[id] = struct{}{}
}

// endMutation clears a pending mutation registered by beginMutation.
func (m *UI) endMutation(id string) {
	delete(m.pendingMutations, id)
}

// mutationsPending reports whether any tracked mutation (tree navigation,
// metadata write, model/provider change, MCP toggle, ...) is still
// in flight.
func (m *UI) mutationsPending() bool {
	return len(m.pendingMutations) > 0
}

// mutationTreeNav identifies the tree/branch navigation chain (legacy
// /tree and /branch dialogs, and drill-in-free leaf moves) as a tracked
// mutation.
const mutationTreeNav = "tree-nav"

// mutationSessionMetadata identifies session metadata writes (model,
// reasoning effort, lazy-MCP toggle) tied to the current leaf as a
// tracked mutation.
const mutationSessionMetadata = "session-metadata"

// mutationDoneMsg reports completion of a command wrapped by
// trackMutation. msg carries the wrapped command's own result, if any, so
// it can still be routed through Update.
type mutationDoneMsg struct {
	id  string
	msg tea.Msg
}

// trackMutation registers id as pending before cmd runs and clears it once
// cmd's result is available, forwarding that result back into Update. Use
// this for simple fire-and-forget commands (metadata writes, MCP toggles)
// that must block branch preview/submission until they land.
func (m *UI) trackMutation(id string, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	m.beginMutation(id)
	return func() tea.Msg {
		return mutationDoneMsg{id: id, msg: cmd()}
	}
}

// dispatchMsg routes msg back through Update, discarding the resulting
// model (always m). Used to unwrap messages carried inside another
// message, such as mutationDoneMsg.
func (m *UI) dispatchMsg(msg tea.Msg) tea.Cmd {
	_, cmd := m.Update(msg)
	return cmd
}

// newSessionGuarded starts a new session unless a branch preview or
// submission is active, in which case it warns instead. Callers that must
// start a new session unconditionally (e.g. recovering from the current
// session being deleted elsewhere) should call m.newSession() directly.
func (m *UI) newSessionGuarded() tea.Cmd {
	if m.branchActive() {
		return util.ReportWarn("Finish or cancel the current branch before starting a new session.")
	}
	return m.newSession()
}

// eligibleBranchTarget reports why msg cannot be branched from, or ""
// if it is an eligible target. It mirrors the backend's validateBranchParts/
// branchSession checks so the UI can reject obviously ineligible
// selections without a round trip, but the backend remains authoritative.
func eligibleBranchTarget(msg message.Message, supportsImages bool) string {
	if msg.MessageType != "" && msg.MessageType != message.MessageTypeMessage {
		return "Select a user message or a completed assistant reply to branch from."
	}
	switch msg.Role {
	case message.User:
		return unsupportedBranchPartsWarning(msg.Parts, supportsImages)
	case message.Assistant:
		if msg.FinishReason() != message.FinishReasonEndTurn || len(msg.ToolCalls()) != 0 {
			return "Branching from an assistant reply requires a completed response with no tool calls."
		}
	default:
		return "Select a user message or a completed assistant reply to branch from."
	}
	return ""
}

// unsupportedBranchPartsWarning reports why parts cannot be branched from,
// or "" if every part is a supported kind (text, an in-capability binary
// attachment, or the terminal Finish marker).
func unsupportedBranchPartsWarning(parts []message.ContentPart, supportsImages bool) string {
	for _, part := range parts {
		switch p := part.(type) {
		case message.TextContent, message.Finish:
		case message.BinaryContent:
			att := message.Attachment{MimeType: p.MIMEType}
			if !att.IsText() && (!att.IsImage() || !supportsImages) {
				return "This message has an attachment type that can't be branched from."
			}
		default:
			return "This message can't be branched from (unsupported content)."
		}
	}
	return ""
}

// shortMessageID returns a short, human-readable fragment of a message ID
// for the branch banner.
func shortMessageID(id string) string {
	const n = 8
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// tryStartBranchPreview handles Shift+B from the root chat: it validates
// eligibility, snapshots the current composer/history/focus state, and
// prefills the composer for editing. No leaf movement or DB write happens
// here.
func (m *UI) tryStartBranchPreview() tea.Cmd {
	if !m.hasSession() || m.session.ParentSessionID != "" || m.isDrilledIn() {
		return util.ReportWarn("Branching is only available in the root chat.")
	}
	if m.mutationsPending() {
		return util.ReportWarn("Please wait for the pending operation to finish before branching.")
	}
	if m.branchPreview != nil {
		return util.ReportWarn("Already previewing a branch. Press Esc to cancel or Enter to send.")
	}
	if m.branchRun != nil {
		return util.ReportWarn("A branch is being submitted; please wait.")
	}
	if m.branchReturn != nil && !m.branchReturn.originalDraft.isEmpty() {
		return util.ReportWarn("Return to pre-branch conversation, then send or clear its draft before branching again.")
	}
	if m.isAgentBusy() || m.com.Workspace.AgentQueuedPrompts(m.session.ID) > 0 {
		return util.ReportWarn("Agent is busy; branching is only available when idle.")
	}

	item := m.activeChat().SelectedItem()
	provider, ok := item.(chat.SourceMessageProvider)
	if !ok {
		return util.ReportWarn("Select a user message or a completed assistant reply to branch from.")
	}
	src := provider.SourceMessage()
	if warn := eligibleBranchTarget(src, m.currentModelSupportsImages()); warn != "" {
		return util.ReportWarn(warn)
	}

	preview := &branchPreview{
		targetID:       src.ID,
		targetRole:     src.Role,
		sessionID:      m.session.ID,
		expectedLeafID: m.session.LeafMessageID,
		shortID:        shortMessageID(src.ID),
		originalDraft: composerSnapshot{
			text:        m.textarea.Value(),
			attachments: append([]message.Attachment(nil), m.attachments.List()...),
			skills:      append([]attachments.SkillAttachment(nil), m.attachments.SkillList()...),
		},
		originalHistory: historySnapshot{
			messages: append([]string(nil), m.promptHistory.messages...),
			index:    m.promptHistory.index,
			draft:    m.promptHistory.draft,
		},
		originalFocus: m.focus,
	}
	m.branchPreview = preview

	prevHeight := m.textarea.Height()
	m.textarea.Reset()
	m.attachments.Reset()
	if src.Role == message.User {
		m.textarea.InsertString(src.Content().Text)
		for _, bc := range src.BinaryContent() {
			m.attachments.Update(message.Attachment{
				FilePath: bc.Path,
				FileName: filepath.Base(bc.Path),
				MimeType: bc.MIMEType,
				Content:  append([]byte(nil), bc.Data...),
			})
		}
	}
	m.textarea.MoveToEnd()
	m.focus = uiFocusEditor

	return tea.Batch(m.textarea.Focus(), m.handleTextareaHeightChange(prevHeight))
}

// cancelBranchPreview restores the exact pre-branch composer, prompt
// history and focus state. It performs no IO or persistent mutation.
func (m *UI) cancelBranchPreview() tea.Cmd {
	preview := m.branchPreview
	if preview == nil {
		return nil
	}
	m.branchPreview = nil
	return m.restoreComposerAndHistory(preview.originalDraft, preview.originalHistory, preview.originalFocus)
}

// restoreComposerAndHistory replaces the composer, attachments and
// prompt-history state with draft/hist, then restores focus. Shared by
// preview cancellation and "Return to pre-branch conversation".
func (m *UI) restoreComposerAndHistory(draft composerSnapshot, hist historySnapshot, focus uiFocusState) tea.Cmd {
	prevHeight := m.textarea.Height()
	m.textarea.Reset()
	m.textarea.InsertString(draft.text)
	m.textarea.MoveToEnd()
	m.attachments.Reset()
	for _, att := range draft.attachments {
		m.attachments.Update(att)
	}
	for _, sk := range draft.skills {
		m.attachments.Update(sk)
	}
	m.promptHistory.messages = hist.messages
	m.promptHistory.index = hist.index
	m.promptHistory.draft = hist.draft
	m.focus = focus

	return tea.Batch(m.textarea.Focus(), m.handleTextareaHeightChange(prevHeight))
}

// branchBannerLine1 renders the first banner line naming the branch target.
func (p *branchPreview) branchBannerLine1() string {
	role := "user"
	if p.targetRole == message.Assistant {
		role = "assistant"
	}
	return "Branching from " + role + " " + p.shortID + "; later messages won't be sent"
}

// branchBanner returns the 1-2 line banner text shown above the composer
// while a branch preview or submission is active, or "" when neither is
// active. Once the first persisted snapshot has loaded (m.branchLoading
// clears), the streaming transcript itself is the indicator and no
// banner is shown.
func (m *UI) branchBanner() string {
	if m.branchPreview != nil {
		return m.branchPreview.branchBannerLine1() + "\n" + branchBannerLine2
	}
	if m.branchLoading {
		return "Branching…"
	}
	return ""
}

// branchBannerHeight returns the number of rows banner occupies (0 when
// there is no active branch preview/submission), for layout height
// calculations.
func branchBannerHeight(banner string) int {
	if banner == "" {
		return 0
	}
	return strings.Count(banner, "\n") + 1
}

// renderBranchBanner renders text truncated per-line to width using
// ANSI/Unicode-aware measurement, styled with the semantic info message
// style shared with the status bar.
func renderBranchBanner(indStyle, msgStyle lipgloss.Style, width int, text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		avail := max(0, width-lipgloss.Width(indStyle.String()))
		lines[i] = indStyle.String() + msgStyle.Render(ansi.Truncate(line, avail, "…"))
	}
	return strings.Join(lines, "\n")
}

// branchLoadingView renders the placeholder shown in the main chat area
// from submission until the first persisted branch snapshot arrives.
func (m *UI) branchLoadingView(width int) string {
	return lipgloss.NewStyle().Width(width).Align(lipgloss.Center).Render("Branching…")
}

// trySubmitBranch handles Enter while a branch preview is active. It
// runs before ordinary textarea reset, slash expansion or quit parsing:
// "/tree", "/branch" and "quit" are sent as literal text. Validation
// never clears the composer; the payload is only frozen (and later
// cleared, once, on acceptance) after it passes.
func (m *UI) trySubmitBranch() tea.Cmd {
	preview := m.branchPreview
	if preview == nil || m.branchRun != nil {
		return nil
	}

	value := strings.TrimSpace(m.textarea.Value())
	fileAttachments := append([]message.Attachment(nil), m.attachments.List()...)
	skillAttachments := m.attachments.SkillList()
	if value == "" && !message.ContainsTextAttachment(fileAttachments) {
		if len(skillAttachments) > 0 {
			return util.ReportInfo("Type a message to send with the attached skill(s).")
		}
		return util.ReportWarn("Type a message to branch with.")
	}
	if len(skillAttachments) > 0 {
		var parts []string
		for _, skill := range skillAttachments {
			parts = append(parts, skills.FormatContentXML(skill.Name, skill.Instructions))
		}
		value = strings.Join(parts, "\n\n") + "\n\n" + value
	}

	ws := m.com.Workspace
	sessionID := preview.sessionID
	run := &branchRun{
		sessionID: sessionID,
		outcome:   make(chan branchOutcome, 2),
		preview:   preview,
	}
	m.branchPreview = nil
	m.branchRun = run
	m.branchLoading = true

	origin := agent.BranchOrigin{TargetMessageID: preview.targetID, ExpectedSourceLeafID: preview.expectedLeafID}
	outcomeCh := run.outcome

	runCmd := func() tea.Msg {
		opts := agent.BranchRunOptions{
			Origin: origin,
			OnUserMessageCreated: func(created message.Message) {
				outcomeCh <- branchOutcome{kind: branchOutcomeAccepted, userID: created.ID}
			},
		}
		err := ws.AgentRunFromMessage(context.Background(), sessionID, value, opts, fileAttachments...)
		outcomeCh <- branchOutcome{kind: branchOutcomeFinished, err: err}
		return nil
	}

	return tea.Batch(runCmd, waitBranchOutcomeCmd(run, outcomeCh))
}

// waitBranchOutcomeCmd blocks on run's outcome channel and wraps the next
// item with run's identity so stale results from a superseded/torn-down
// run can be detected and ignored.
func waitBranchOutcomeCmd(run *branchRun, ch chan branchOutcome) tea.Cmd {
	return func() tea.Msg {
		out, ok := <-ch
		if !ok {
			return nil
		}
		return branchOutcomeMsg{run: run, outcome: out}
	}
}

// handleBranchOutcome processes one accepted/finished outcome. It is the
// only path that ever advances a branchRun's accepted/finished state.
func (m *UI) handleBranchOutcome(msg branchOutcomeMsg) tea.Cmd {
	run := msg.run
	if m.branchRun != run {
		// Superseded by teardown or (should never happen) a new run;
		// never restore a draft into another session/workspace.
		return nil
	}
	switch msg.outcome.kind {
	case branchOutcomeAccepted:
		run.accepted = true
		run.acceptedUserID = msg.outcome.userID
		m.saveBranchReturnSnapshot(run)
		clearCmd := m.clearBranchComposer()
		return tea.Batch(
			clearCmd,
			m.scheduleBranchRead(run),
			waitBranchOutcomeCmd(run, run.outcome),
			branchPollCmd(run),
		)
	case branchOutcomeFinished:
		run.finished = true
		run.finishErr = msg.outcome.err
		if !run.accepted {
			// Ordered synchronous callback makes this unambiguously
			// pre-insert: restore the editable preview with its
			// payload intact rather than resubmitting or rolling back.
			return m.revertBranchRunToPreview(run, msg.outcome.err)
		}
		// Whole-run finished always forces a fresh final snapshot,
		// regardless of events received; the poll loop started at
		// acceptance keeps running to pick this up (and any later
		// queued turn) even if every pubsub event drops.
		run.dirty = true
		return m.scheduleBranchRead(run)
	}
	return nil
}

// revertBranchRunToPreview restores the editable preview after a
// pre-acceptance failure (validation, busy, cancellation).
func (m *UI) revertBranchRunToPreview(run *branchRun, err error) tea.Cmd {
	if m.branchRun != run {
		return nil
	}
	m.branchRun = nil
	m.branchLoading = false
	m.branchPreview = run.preview
	if err != nil && !errors.Is(err, context.Canceled) {
		return util.ReportError(fmt.Errorf("branch not sent: %w", err))
	}
	return nil
}

// clearBranchComposer clears the submitted composer/attachments exactly
// once, on acceptance.
func (m *UI) clearBranchComposer() tea.Cmd {
	prevHeight := m.textarea.Height()
	m.textarea.Reset()
	m.attachments.Reset()
	return m.handleTextareaHeightChange(prevHeight)
}

// scheduleBranchRead issues one serialized GetSession+GetBranchPath read
// for run if none is already in flight; otherwise it just marks run
// dirty so the in-flight read's completion schedules the next one.
func (m *UI) scheduleBranchRead(run *branchRun) tea.Cmd {
	if run == nil {
		return nil
	}
	if run.reading {
		run.dirty = true
		return nil
	}
	run.reading = true
	run.dirty = false
	return m.branchReadCmd(run)
}

// branchReadCmd performs the actual IO for one reconciliation read. Read
// bodies from pubsub are never trusted; only this persisted read (and
// the equivalent one in beginBranchReturn) may install a new transcript.
func (m *UI) branchReadCmd(run *branchRun) tea.Cmd {
	ws := m.com.Workspace
	sessionID := run.sessionID
	acceptedID := run.acceptedUserID
	return func() tea.Msg {
		ctx := context.Background()
		sess, err := ws.GetSession(ctx, sessionID)
		if err != nil {
			return branchReadResultMsg{run: run, err: err}
		}
		var msgs []message.Message
		if sess.LeafMessageID != "" {
			msgs, err = ws.GetBranchPath(ctx, sess.LeafMessageID)
			if err != nil {
				return branchReadResultMsg{run: run, err: err}
			}
		}
		if acceptedID != "" && !branchPathContainsID(msgs, acceptedID) {
			return branchReadResultMsg{run: run, err: fmt.Errorf("accepted branch message %s not yet in persisted path", acceptedID)}
		}
		return branchReadResultMsg{run: run, session: &sess, messages: msgs}
	}
}

// branchPathContainsID reports whether id appears in path.
func branchPathContainsID(path []message.Message, id string) bool {
	for _, m := range path {
		if m.ID == id {
			return true
		}
	}
	return false
}

// handleBranchReadResult installs a persisted snapshot (or retries on
// error), and chains the next read if more dirtiness arrived meanwhile.
func (m *UI) handleBranchReadResult(msg branchReadResultMsg) tea.Cmd {
	run := msg.run
	if m.branchRun != run {
		return nil
	}
	run.reading = false

	if msg.err != nil {
		run.failCount++
		run.dirty = true
		return nil // the still-running poll loop retries this run
	}
	run.failCount = 0
	m.branchLoading = false
	if run.finished {
		run.reconciledAfterFinish = true
	}

	m.session = msg.session
	m.clearDrillStack()
	var cmds []tea.Cmd
	if cmd := m.setSessionMessages(msg.messages); cmd != nil {
		cmds = append(cmds, cmd)
	}
	m.renderPills()

	if run.dirty {
		if cmd := m.scheduleBranchRead(run); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// branchPollCmd schedules the next reconciliation tick for run.
func branchPollCmd(run *branchRun) tea.Cmd {
	return tea.Tick(branchPollInterval, func(time.Time) tea.Msg {
		return branchPollMsg{run: run}
	})
}

// handleBranchPoll forces dirtiness (repairing any dropped pubsub event
// or retrying a failed read) and reschedules itself. It stops the moment
// m.branchRun no longer points at run, i.e. once the user explicitly
// navigates away or replaces it with a new run.
func (m *UI) handleBranchPoll(msg branchPollMsg) tea.Cmd {
	run := msg.run
	if m.branchRun != run {
		return nil
	}
	run.dirty = true
	cmds := []tea.Cmd{branchPollCmd(run)}
	if cmd := m.scheduleBranchRead(run); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// clearBranchState resets preview/run/loading state on explicit
// navigation away from the branch (new session, session switch, tree
// navigation, or a successful "Return to pre-branch conversation"). The
// recoverable branchReturn snapshot is intentionally NOT cleared here:
// it is workspace-local and outlives session navigation.
func (m *UI) clearBranchState() {
	m.branchPreview = nil
	m.branchRun = nil
	m.branchLoading = false
}

// saveBranchReturnSnapshot records the single recoverable pre-branch
// snapshot on acceptance. A nonempty existing draft is never replaced
// (tryStartBranchPreview already refuses a second B in that case); an
// empty one may be replaced by a later branch's acceptance.
func (m *UI) saveBranchReturnSnapshot(run *branchRun) {
	if m.branchReturn != nil && !m.branchReturn.originalDraft.isEmpty() {
		return
	}
	m.branchReturn = &branchReturnSnapshot{
		sessionID:       run.preview.sessionID,
		leafID:          run.preview.expectedLeafID,
		originalDraft:   run.preview.originalDraft,
		originalHistory: run.preview.originalHistory,
		originalFocus:   run.preview.originalFocus,
	}
}

// beginBranchReturn handles the "Return to pre-branch conversation"
// palette action. All conflict checks happen before any IO or MoveLeaf;
// on conflict the snapshot is retained untouched.
func (m *UI) beginBranchReturn() tea.Cmd {
	snap := m.branchReturn
	if snap == nil {
		return util.ReportWarn("No pre-branch conversation to return to.")
	}
	if m.branchActive() {
		return util.ReportWarn("Finish or cancel the current branch before returning.")
	}
	if m.mutationsPending() {
		return util.ReportWarn("Please wait for the pending operation to finish before returning.")
	}
	if !m.hasSession() {
		return util.ReportWarn("No active session.")
	}
	if m.textarea.Value() != "" || m.attachments.HasContent() {
		return util.ReportWarn("Clear the composer before returning to the pre-branch conversation.")
	}
	if m.isAgentBusy() || m.com.Workspace.AgentQueuedPrompts(m.session.ID) > 0 {
		return util.ReportWarn("Agent is busy; return is only available when idle.")
	}

	m.beginMutation(mutationBranchReturn)
	ws := m.com.Workspace
	sessionID := snap.sessionID
	leafID := snap.leafID
	return func() tea.Msg {
		ctx := context.Background()
		// Validate the exact destination exists before mutating
		// anything (an empty leaf means an empty path, which is
		// valid: a root-parent branch target).
		if leafID != "" {
			if _, err := ws.GetBranchPath(ctx, leafID); err != nil {
				return branchReturnResultMsg{err: err}
			}
		}
		if err := ws.MoveLeaf(ctx, sessionID, leafID); err != nil {
			return branchReturnResultMsg{err: err}
		}
		sess, err := ws.GetSession(ctx, sessionID)
		if err != nil {
			return branchReturnResultMsg{err: err, movedLeaf: true}
		}
		var msgs []message.Message
		if leafID != "" {
			msgs, err = ws.GetBranchPath(ctx, leafID)
			if err != nil {
				return branchReturnResultMsg{err: err, movedLeaf: true}
			}
		}
		return branchReturnResultMsg{session: &sess, messages: msgs}
	}
}

// handleBranchReturnResult installs the exact saved leaf/session and
// restores the full saved draft/history/focus, then consumes the
// snapshot. On any failure the snapshot is retained; a leaf that moved
// before a later read failed is reported distinctly so the user knows
// not to submit again, only to retry the read/navigate manually.
func (m *UI) handleBranchReturnResult(msg branchReturnResultMsg) tea.Cmd {
	m.endMutation(mutationBranchReturn)
	if msg.err != nil {
		if msg.movedLeaf {
			return util.ReportError(fmt.Errorf("pre-branch leaf restored but reload failed, use the session tree to retry: %w", msg.err))
		}
		return util.ReportError(fmt.Errorf("return to pre-branch conversation failed: %w", msg.err))
	}

	snap := m.branchReturn
	m.session = msg.session
	m.clearDrillStack()
	m.clearBranchState()

	var cmds []tea.Cmd
	if cmd := m.setSessionMessages(msg.messages); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if snap != nil {
		cmds = append(cmds, m.restoreComposerAndHistory(snap.originalDraft, snap.originalHistory, snap.originalFocus))
	}
	m.branchReturn = nil
	m.updateLayoutAndSize()
	return tea.Batch(cmds...)
}

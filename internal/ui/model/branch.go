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

const (
	branchBannerLine2 = "Enter send · Esc cancel"
)

type composerSnapshot struct {
	text        string
	attachments []message.Attachment
	skills      []attachments.SkillAttachment
}

func (c composerSnapshot) isEmpty() bool {
	return c.text == "" && len(c.attachments) == 0 && len(c.skills) == 0
}

type historySnapshot struct {
	messages []string
	index    int
	draft    string
}

type branchPreview struct {
	targetID       string
	targetRole     message.MessageRole
	sessionID      string
	expectedLeafID string
	shortID        string
	invalid        bool

	originalDraft    composerSnapshot
	originalHistory  historySnapshot
	originalFocus    uiFocusState
	originalViewport branchViewport
}

type branchRun struct {
	sessionID string
	outcome   chan branchOutcome
	preview   *branchPreview

	accepted       bool
	acceptedUserID string
	finished       bool
	finishErr      error

	reconciledAfterFinish bool

	dirty           bool
	reading         bool
	failCount       int
	items           map[string]branchItemSnapshot
	ctx             context.Context
	cancel          context.CancelFunc
	runCancel       context.CancelFunc
	readAfterFinish bool
	readEpoch       uint64
	retryPending    bool
	reloadErr       error
	returning       bool
}

type branchOutcomeKind int

const (
	branchOutcomeAccepted branchOutcomeKind = iota
	branchOutcomeFinished
)

type branchOutcome struct {
	kind   branchOutcomeKind
	userID string
	err    error
}

type branchOutcomeMsg struct {
	run     *branchRun
	outcome branchOutcome
}

type branchReadResultMsg struct {
	run         *branchRun
	session     *session.Session
	messages    []message.Message
	nested      map[string]branchNestedSnapshot
	afterFinish bool
	epoch       uint64
	err         error
}

type branchRetryMsg struct{ run *branchRun }

type branchPollMsg struct {
	run *branchRun
}

const branchPollInterval = 200 * time.Millisecond

type branchReturnSnapshot struct {
	sessionID        string
	leafID           string
	originalDraft    composerSnapshot
	originalHistory  historySnapshot
	originalFocus    uiFocusState
	originalViewport branchViewport
}

type branchReturnResultMsg struct {
	run       *branchRun
	nested    map[string]branchNestedSnapshot
	session   *session.Session
	messages  []message.Message
	err       error
	movedLeaf bool
}

const mutationBranchReturn = "branch-return"

func (m *UI) branchActive() bool {
	if m.branchLoading {
		return true
	}
	if m.branchPreview != nil {
		return true
	}
	if m.branchRun == nil {
		return false
	}
	return m.branchRun.reloadErr != nil || !(m.branchRun.finished && m.branchRun.reconciledAfterFinish)
}

func (m *UI) beginMutation(id string) {
	if m.pendingMutations == nil {
		m.pendingMutations = make(map[string]struct{})
	}
	m.pendingMutations[id] = struct{}{}
}

func (m *UI) endMutation(id string) {
	delete(m.pendingMutations, id)
}

func (m *UI) mutationsPending() bool {
	return len(m.pendingMutations) > 0
}

const mutationTreeNav = "tree-nav"

const mutationSessionMetadata = "session-metadata"

type mutationDoneMsg struct {
	id        string
	kind      string
	sessionID string
	msg       tea.Msg
}

func (m *UI) trackMutation(id string, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	kind := id
	if kind == mutationSessionMetadata || kind == "model-refresh" {
		m.mutationSequence++
		id = fmt.Sprintf("%s-%d", id, m.mutationSequence)
	}
	sessionID := ""
	if m.session != nil {
		sessionID = m.session.ID
	}
	m.beginMutation(id)
	return func() tea.Msg {
		return mutationDoneMsg{id: id, kind: kind, sessionID: sessionID, msg: cmd()}
	}
}

func (m *UI) dispatchMsg(msg tea.Msg) tea.Cmd {
	_, cmd := m.Update(msg)
	return cmd
}

func (m *UI) newSessionGuarded() tea.Cmd {
	if m.branchActive() || m.mutationsPending() {
		return util.ReportWarn("Finish or cancel the current branch before starting a new session.")
	}
	return m.newSession()
}

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

func shortMessageID(id string) string {
	const n = 8
	if len(id) <= n {
		return id
	}
	return id[:n]
}

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
	if m.branchActive() {
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
		originalFocus:    m.focus,
		originalViewport: m.chat.branchViewport(),
	}
	m.clearBranchState()
	m.branchPreview = preview
	m.chat.SetFollow(false)

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

	cmd := tea.Batch(m.textarea.Focus(), m.handleTextareaHeightChange(prevHeight))
	m.chat.restoreBranchViewport(preview.originalViewport)
	m.chat.SetFollow(false)
	return cmd
}

func (m *UI) cancelBranchPreview() tea.Cmd {
	preview := m.branchPreview
	if preview == nil {
		return nil
	}
	m.branchPreview = nil
	cmd := m.restoreComposerAndHistory(preview.originalDraft, preview.originalHistory, preview.originalFocus)
	m.updateLayoutAndSize()
	m.chat.restoreBranchViewport(preview.originalViewport)
	return cmd
}

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

func (p *branchPreview) branchBannerLine1() string {
	role := "user"
	if p.targetRole == message.Assistant {
		role = "assistant"
	}
	return "Branching from " + role + " " + p.shortID + "; later messages won't be sent"
}

func (m *UI) branchBanner() string {
	if m.branchRun != nil && m.branchRun.reloadErr != nil {
		return "Branch reload failed · Ctrl+P → Retry branch reload"
	}
	if m.branchPreview != nil {
		return m.branchPreview.branchBannerLine1() + "\n" + branchBannerLine2
	}
	if m.branchLoading {
		return "Branching…"
	}
	return ""
}

func branchBannerHeight(banner string) int {
	if banner == "" {
		return 0
	}
	return strings.Count(banner, "\n") + 1
}

func renderBranchBanner(indStyle, msgStyle lipgloss.Style, width int, text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		avail := max(0, width-lipgloss.Width(indStyle.String()))
		lines[i] = indStyle.String() + msgStyle.Render(ansi.Truncate(line, avail, "…"))
	}
	return strings.Join(lines, "\n")
}

func (m *UI) branchLoadingView(width int) string {
	text := "Branching…"
	if m.branchRun != nil && m.branchRun.reloadErr != nil {
		text = "Branch reload failed. Use Retry branch reload in the command palette."
	}
	return lipgloss.NewStyle().Width(width).Align(lipgloss.Center).Render(text)
}

func (m *UI) trySubmitBranch() tea.Cmd {
	preview := m.branchPreview
	if preview == nil || m.branchRun != nil {
		return nil
	}

	if preview.invalid {
		return util.ReportWarn("Branch source changed. Press Esc, reselect the source message, then press B to retry.")
	}
	if m.mutationsPending() {
		return util.ReportWarn("Please wait for the pending operation to finish before sending.")
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
	ctx, cancel := context.WithCancel(context.Background())
	runCtx, runCancel := context.WithCancel(context.Background())
	run := &branchRun{
		ctx: ctx, cancel: cancel, runCancel: runCancel,
		sessionID: sessionID,
		outcome:   make(chan branchOutcome, 2),
		preview:   preview,
	}
	m.branchPreview = nil
	m.branchRun = run
	m.branchLoading = true
	m.updateLayoutAndSize()

	origin := agent.BranchOrigin{TargetMessageID: preview.targetID, ExpectedSourceLeafID: preview.expectedLeafID}
	outcomeCh := run.outcome

	runCmd := func() tea.Msg {
		opts := agent.BranchRunOptions{
			Origin: origin,
			OnUserMessageCreated: func(created message.Message) {
				outcomeCh <- branchOutcome{kind: branchOutcomeAccepted, userID: created.ID}
			},
		}
		err := ws.AgentRunFromMessage(runCtx, sessionID, value, opts, fileAttachments...)
		outcomeCh <- branchOutcome{kind: branchOutcomeFinished, err: err}
		return nil
	}

	return tea.Batch(runCmd, waitBranchOutcomeCmd(run, outcomeCh))
}

func waitBranchOutcomeCmd(run *branchRun, ch chan branchOutcome) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-run.ctx.Done():
			return nil
		case out, ok := <-ch:
			if !ok {
				return nil
			}
			return branchOutcomeMsg{run: run, outcome: out}
		}
	}
}

func (m *UI) handleBranchOutcome(msg branchOutcomeMsg) tea.Cmd {
	run := msg.run
	if m.branchRun != run {
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
			return m.revertBranchRunToPreview(run, msg.outcome.err)
		}
		run.dirty = true
		read := m.scheduleBranchRead(run)
		if msg.outcome.err != nil && !errors.Is(msg.outcome.err, context.Canceled) {
			return tea.Batch(read, util.ReportError(msg.outcome.err))
		}
		return read
	}
	return nil
}

func (m *UI) revertBranchRunToPreview(run *branchRun, err error) tea.Cmd {
	if m.branchRun != run {
		return nil
	}
	if run.cancel != nil {
		run.cancel()
	}
	if run.runCancel != nil {
		run.runCancel()
	}
	m.branchRun = nil
	m.branchLoading = false
	m.branchPreview = run.preview
	if errors.Is(err, agent.ErrBranchStaleSource) || errors.Is(err, agent.ErrBranchInvalidTarget) {
		m.branchPreview.invalid = true
		return util.ReportWarn("Branch source changed. Your draft is preserved; press Esc, reselect the source message, then press B to retry.")
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return util.ReportError(fmt.Errorf("branch not sent: %w", err))
	}
	return nil
}

func (m *UI) clearBranchComposer() tea.Cmd {
	prevHeight := m.textarea.Height()
	m.textarea.Reset()
	m.attachments.Reset()
	return m.handleTextareaHeightChange(prevHeight)
}

func (m *UI) scheduleBranchRead(run *branchRun) tea.Cmd {
	if run == nil {
		return nil
	}
	if run.retryPending || run.reloadErr != nil {
		run.dirty = true
		return nil
	}
	if run.reading {
		run.dirty = true
		return nil
	}
	run.readEpoch++
	run.reading = true
	run.readAfterFinish = run.finished
	run.dirty = false
	return m.branchReadCmd(run)
}

func (m *UI) branchReadCmd(run *branchRun) tea.Cmd {
	ws := m.com.Workspace
	sessionID := run.sessionID
	acceptedID := run.acceptedUserID
	afterFinish := run.readAfterFinish
	epoch := run.readEpoch
	ctx := run.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		result := branchReadResultMsg{run: run, afterFinish: afterFinish, epoch: epoch}
		for range 3 {
			sess, err := ws.GetSession(ctx, sessionID)
			if err != nil {
				result.err = err
				return result
			}
			var msgs []message.Message
			if sess.LeafMessageID != "" {
				msgs, err = ws.GetBranchPath(ctx, sess.LeafMessageID)
				if err != nil {
					result.err = err
					return result
				}
			}
			nested, err := readBranchNested(ctx, ws, msgs)
			if err != nil {
				result.err = err
				return result
			}
			latest, err := ws.GetSession(ctx, sessionID)
			if err != nil {
				result.err = err
				return result
			}
			if latest.LeafMessageID != sess.LeafMessageID {
				continue
			}
			if acceptedID != "" && !branchPathContainsID(msgs, acceptedID) {
				result.err = fmt.Errorf("accepted branch message %s not yet in persisted path", acceptedID)
				return result
			}
			result.session, result.messages, result.nested = &latest, msgs, nested
			return result
		}
		result.err = errors.New("branch leaf changed during reload")
		return result
	}
}

func branchPathContainsID(path []message.Message, id string) bool {
	for _, m := range path {
		if m.ID == id {
			return true
		}
	}
	return false
}

func (m *UI) handleBranchReadResult(msg branchReadResultMsg) tea.Cmd {
	run := msg.run
	if m.branchRun != run || run == nil || msg.epoch != run.readEpoch || !run.reading {
		return nil
	}
	run.reading = false

	if msg.err != nil {
		run.failCount++
		run.dirty = true
		delays := [...]time.Duration{100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond}
		if run.failCount <= len(delays) {
			run.retryPending = true
			return tea.Tick(delays[run.failCount-1], func(time.Time) tea.Msg { return branchRetryMsg{run: run} })
		}
		run.reloadErr = msg.err
		m.updateLayoutAndSize()
		return util.ReportError(fmt.Errorf("branch reload failed; use Retry branch reload in the command palette: %w", msg.err))
	}
	run.failCount = 0
	run.reloadErr = nil
	if run.returning {
		return m.finishBranchReturn(msg)
	}
	wasLoading := m.branchLoading
	m.branchLoading = false
	if wasLoading {
		m.updateLayoutAndSize()
	}
	if msg.afterFinish {
		run.reconciledAfterFinish = true
	}

	m.session = msg.session
	m.installBranchSnapshot(msg.messages, msg.nested, run)
	var cmds []tea.Cmd
	m.renderPills()
	if m.isAgentBusy() {
		for _, item := range run.items {
			for _, rendered := range item.items {
				if a, ok := rendered.(chat.Animatable); ok {
					cmds = append(cmds, a.StartAnimation())
				}
			}
		}
	}

	if run.dirty {
		if cmd := m.scheduleBranchRead(run); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func branchPollCmd(run *branchRun) tea.Cmd {
	return tea.Tick(branchPollInterval, func(time.Time) tea.Msg {
		return branchPollMsg{run: run}
	})
}

func (m *UI) handleBranchRetry(msg branchRetryMsg) tea.Cmd {
	if m.branchRun != msg.run || !msg.run.retryPending {
		return nil
	}
	msg.run.retryPending = false
	return m.scheduleBranchRead(msg.run)
}

func (m *UI) retryBranchReload() tea.Cmd {
	run := m.branchRun
	if run == nil || run.reloadErr == nil {
		return nil
	}
	run.failCount = 0
	run.reloadErr = nil
	m.updateLayoutAndSize()
	return m.scheduleBranchRead(run)
}

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

func (m *UI) clearBranchState() {
	if run := m.branchRun; run != nil {
		if run.cancel != nil {
			run.cancel()
		}
		if run.runCancel != nil {
			run.runCancel()
		}
	}
	m.branchPreview = nil
	m.branchRun = nil
	m.branchLoading = false
}

func (m *UI) saveBranchReturnSnapshot(run *branchRun) {
	if m.branchReturn != nil && !m.branchReturn.originalDraft.isEmpty() {
		return
	}
	m.branchReturn = &branchReturnSnapshot{
		sessionID:        run.preview.sessionID,
		leafID:           run.preview.expectedLeafID,
		originalDraft:    run.preview.originalDraft,
		originalHistory:  run.preview.originalHistory,
		originalFocus:    run.preview.originalFocus,
		originalViewport: run.preview.originalViewport,
	}
}

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
	if m.isAgentBusy() || m.com.Workspace.AgentQueuedPrompts(m.session.ID) > 0 || m.com.Workspace.AgentIsSessionBusy(snap.sessionID) || m.com.Workspace.AgentQueuedPrompts(snap.sessionID) > 0 {
		return util.ReportWarn("Agent is busy; return is only available when idle.")
	}

	m.clearBranchState()
	ctx, cancel := context.WithCancel(context.Background())
	run := &branchRun{sessionID: snap.sessionID, ctx: ctx, cancel: cancel, returning: true, finished: true, reading: true, readAfterFinish: true}
	m.branchRun = run
	m.branchLoading = true
	m.beginMutation(mutationBranchReturn)
	ws := m.com.Workspace
	sessionID := snap.sessionID
	leafID := snap.leafID
	read := m.branchReadCmd(run)
	return func() tea.Msg {
		if leafID != "" {
			if _, err := ws.GetBranchPath(ctx, leafID); err != nil {
				return branchReturnResultMsg{run: run, err: err}
			}
		}
		if err := ws.MoveLeaf(ctx, sessionID, leafID); err != nil {
			return branchReturnResultMsg{run: run, err: err}
		}
		result := read().(branchReadResultMsg)
		return branchReturnResultMsg{run: run, session: result.session, messages: result.messages, nested: result.nested, err: result.err, movedLeaf: true}
	}
}

func (m *UI) handleBranchReturnResult(msg branchReturnResultMsg) tea.Cmd {
	if m.branchRun != msg.run {
		return nil
	}
	if msg.err != nil && !msg.movedLeaf {
		m.endMutation(mutationBranchReturn)
		m.clearBranchState()
		return util.ReportError(fmt.Errorf("return to pre-branch conversation failed: %w", msg.err))
	}
	return m.handleBranchReadResult(branchReadResultMsg{run: msg.run, session: msg.session, messages: msg.messages, nested: msg.nested, err: msg.err, afterFinish: true, epoch: msg.run.readEpoch})
}

func (m *UI) finishBranchReturn(msg branchReadResultMsg) tea.Cmd {
	snap := m.branchReturn
	m.endMutation(mutationBranchReturn)
	m.session = msg.session
	m.clearDrillStack()
	m.installBranchSnapshot(msg.messages, msg.nested, nil)
	m.clearBranchState()
	var cmd tea.Cmd
	if snap != nil {
		cmd = m.restoreComposerAndHistory(snap.originalDraft, snap.originalHistory, snap.originalFocus)
	}
	m.branchReturn = nil
	m.updateLayoutAndSize()
	if snap != nil {
		m.chat.restoreBranchViewport(snap.originalViewport)
	}
	return cmd
}

package model

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
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
type branchRun struct {
	sessionID string
	finished  bool
}

// branchReturnSnapshot is the single recoverable pre-branch snapshot kept
// after a branch is accepted, allowing "Return to pre-branch conversation".
type branchReturnSnapshot struct {
	sessionID     string
	leafID        string
	originalDraft composerSnapshot
}

// branchActive reports whether a branch preview or an in-flight branch
// submission currently owns the composer.
func (m *UI) branchActive() bool {
	return m.branchPreview != nil || (m.branchRun != nil && !m.branchRun.finished)
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

	prevHeight := m.textarea.Height()
	m.textarea.Reset()
	m.textarea.InsertString(preview.originalDraft.text)
	m.textarea.MoveToEnd()
	m.attachments.Reset()
	for _, att := range preview.originalDraft.attachments {
		m.attachments.Update(att)
	}
	for _, sk := range preview.originalDraft.skills {
		m.attachments.Update(sk)
	}
	m.promptHistory.messages = preview.originalHistory.messages
	m.promptHistory.index = preview.originalHistory.index
	m.promptHistory.draft = preview.originalHistory.draft
	m.focus = preview.originalFocus

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
// active.
func (m *UI) branchBanner() string {
	if m.branchPreview != nil {
		return m.branchPreview.branchBannerLine1() + "\n" + branchBannerLine2
	}
	if m.branchRun != nil && !m.branchRun.finished {
		return "Branching…\n" + branchBannerLine2
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

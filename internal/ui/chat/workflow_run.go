package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// WorkflowRunDrillInHandler is implemented by items that open a workflow
// run's view when drilled into.
type WorkflowRunDrillInHandler interface {
	// WorkflowRunDrillIn returns the run to open and the breadcrumb label.
	WorkflowRunDrillIn() (runID, label string)
}

// workflowRunItem is the shared base of the run node and the rows of a run
// view: a focusable, cached, single-purpose renderer.
type workflowRunItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	id  string
	sty *styles.Styles
}

func newWorkflowRunItem(sty *styles.Styles, id string) workflowRunItem {
	v := list.NewVersioned()
	return workflowRunItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		id:                       id,
		sty:                      sty,
	}
}

// ID implements MessageItem.
func (w *workflowRunItem) ID() string { return w.id }

// render caches content built by build and prefixes each line with the
// workflow gutter.
func (w *workflowRunItem) render(width int, build func(width int) string) string {
	key := uint64(0)
	if w.focused {
		key = 1
	}
	useCache := !w.isHighlighted()
	if useCache {
		if cached, ok := w.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	prefix := w.sty.Messages.WorkflowBlurred.Render()
	if w.focused {
		prefix = w.sty.Messages.WorkflowFocused.Render()
	}
	lines := strings.Split(w.rawRender(width, build), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	if useCache {
		w.setCachedPrefixedRender(out, width, key)
	}
	return out
}

func (w *workflowRunItem) rawRender(width int, build func(width int) string) string {
	cappedWidth := cappedMessageWidth(width)
	content, height, ok := w.getCachedRender(cappedWidth)
	if !ok {
		content = build(cappedWidth)
		height = lipgloss.Height(content)
		w.setCachedRender(content, cappedWidth, height)
	}
	return w.renderHighlighted(content, cappedWidth, height)
}

// WorkflowRunMessageItem is a workflow run's node in the session that
// started it: what's running and where it's up to. Drilling in opens the
// run's view.
type WorkflowRunMessageItem struct {
	workflowRunItem

	message *message.Message
	marker  message.WorkflowRunContent
	// view is nil until the run's view has loaded.
	view *workflowhost.RunView
}

// NewWorkflowRunMessageItem creates the node for a workflow_run marker,
// reporting false for any other message.
func NewWorkflowRunMessageItem(sty *styles.Styles, msg *message.Message) (*WorkflowRunMessageItem, bool) {
	if msg.MessageType != message.MessageTypeWorkflowRun {
		return nil, false
	}
	for _, part := range msg.Parts {
		if marker, ok := part.(message.WorkflowRunContent); ok {
			return &WorkflowRunMessageItem{
				workflowRunItem: newWorkflowRunItem(sty, msg.ID),
				message:         msg,
				marker:          marker,
			}, true
		}
	}
	return nil, false
}

// RunID returns the run the node stands for.
func (w *WorkflowRunMessageItem) RunID() string { return w.marker.RunID }

// HasView reports whether the run's view has loaded.
func (w *WorkflowRunMessageItem) HasView() bool { return w.view != nil }

// SetView updates the node from the run's latest view.
func (w *WorkflowRunMessageItem) SetView(view workflowhost.RunView) {
	w.view = &view
	w.clearCache()
	w.Bump()
}

// Finished implements list.Item. A node changes until its run ends.
func (w *WorkflowRunMessageItem) Finished() bool {
	return w.view != nil && w.view.Status != workflowhost.StatusRunning
}

// SourceMessage returns a copy of the underlying message.
func (w *WorkflowRunMessageItem) SourceMessage() message.Message { return w.message.Clone() }

// WorkflowRunDrillIn implements [WorkflowRunDrillInHandler].
func (w *WorkflowRunMessageItem) WorkflowRunDrillIn() (string, string) {
	return w.marker.RunID, "/" + w.marker.Command
}

// HandleKeyEvent implements [KeyEventHandler]: →/l opens the run's view.
func (w *WorkflowRunMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); k == "right" || k == "l" {
		runID, label := w.WorkflowRunDrillIn()
		return true, func() tea.Msg { return util.WorkflowRunDrillInMsg{RunID: runID, Label: label} }
	}
	return false, nil
}

// RawRender implements [MessageItem].
func (w *WorkflowRunMessageItem) RawRender(width int) string {
	return w.rawRender(width, w.build)
}

// Render implements MessageItem.
func (w *WorkflowRunMessageItem) Render(width int) string {
	return w.render(width, w.build)
}

func (w *WorkflowRunMessageItem) build(width int) string {
	sty := w.sty
	title := sty.Messages.WorkflowTag.Render("Workflow") + " " + sty.Messages.WorkflowTitle.Render("/"+w.marker.Command)
	header := workflowStatusIcon(sty, w.status()) + " " + title
	if input := firstLine(w.marker.Input); input != "" {
		header += " " + sty.Tool.ParamMain.Render(ansi.Truncate(input, max(width-lipgloss.Width(header)-1, 0), "…"))
	}
	return lipgloss.JoinVertical(lipgloss.Left, ansi.Truncate(header, width, "…"),
		ansi.Truncate("  "+sty.Tool.StatsLine.Render(w.summary()), width, "…"))
}

func (w *WorkflowRunMessageItem) status() workflowhost.Status {
	if w.view == nil {
		return workflowhost.StatusRunning
	}
	return w.view.Status
}

// summary says where the run is up to while it runs, and how it ended
// once it has.
func (w *WorkflowRunMessageItem) summary() string {
	if w.view == nil {
		return "loading…"
	}
	v := w.view
	if v.Status != workflowhost.StatusRunning {
		return WorkflowOutcomeText(v.Status, outcomeDetail(*v)) + " · " + formatRunDuration(v.FinishedAt.Sub(v.StartedAt))
	}
	parts := []string{}
	if step, detail := v.Current(); step != "" {
		parts = append(parts, step)
		if detail != "" {
			parts = append(parts, detail)
		}
	}
	parts = append(parts, runningState(*v))
	return strings.Join(parts, " · ")
}

// WorkflowRunRowItem is one row of a run view: the header, a phase, or
// something that happened in it. Rows with a session drill into it.
type WorkflowRunRowItem struct {
	workflowRunItem

	line      func(width int) string
	sessionID string
	label     string
}

// AgentDrillIn implements [AgentDrillInHandler].
func (w *WorkflowRunRowItem) AgentDrillIn() string { return w.sessionID }

// AgentDrillInLabel implements [AgentDrillInHandler].
func (w *WorkflowRunRowItem) AgentDrillInLabel() string { return w.label }

// Finished implements list.Item. Rows are rebuilt when the run changes.
func (w *WorkflowRunRowItem) Finished() bool { return true }

// HandleKeyEvent implements [KeyEventHandler]: →/l/enter opens the row's
// session.
func (w *WorkflowRunRowItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); (k == "right" || k == "l" || k == "enter") && w.sessionID != "" {
		sessionID, label := w.sessionID, w.label
		return true, func() tea.Msg { return util.AgentDrillInMsg{SessionID: sessionID, Label: label} }
	}
	return false, nil
}

// RawRender implements [MessageItem].
func (w *WorkflowRunRowItem) RawRender(width int) string { return w.rawRender(width, w.line) }

// Render implements MessageItem.
func (w *WorkflowRunRowItem) Render(width int) string { return w.render(width, w.line) }

// BuildWorkflowRunItems lays out a run's view as rows: a header saying
// what the run is and how it's going, then each phase with what happened
// in it.
func BuildWorkflowRunItems(sty *styles.Styles, command string, view workflowhost.RunView) []MessageItem {
	var rows []MessageItem
	add := func(id, sessionID, label string, line func(width int) string) {
		rows = append(rows, &WorkflowRunRowItem{
			workflowRunItem: newWorkflowRunItem(sty, id),
			line:            line,
			sessionID:       sessionID,
			label:           label,
		})
	}
	add("header", "", "", func(width int) string {
		title := sty.Messages.WorkflowTag.Render("Workflow") + " " + sty.Messages.WorkflowTitle.Render("/"+command)
		if input := firstLine(view.Input); input != "" {
			title += " " + sty.Tool.ParamMain.Render(input)
		}
		state := WorkflowOutcomeText(view.Status, outcomeDetail(view))
		if view.Status == workflowhost.StatusRunning {
			state = runningState(view)
		}
		return ansi.Truncate(title, width, "…") + "\n" + sty.Tool.StatsLine.Render(state)
	})
	for p, phase := range view.Phases {
		if phase.Step != "" {
			label := phase.Step
			if phase.Detail != "" {
				label += " · " + phase.Detail
			}
			add(fmt.Sprintf("phase-%d", p), "", "", func(width int) string {
				return ansi.Truncate(sty.Tool.NameNormal.Render("● "+label), width, "…")
			})
		}
		for i, item := range phase.Items {
			id := fmt.Sprintf("item-%d-%d", p, i)
			name := workflowRunItemName(command, item)
			add(id, item.SessionID, name, func(width int) string {
				return ansi.Truncate("  "+workflowRunItemLine(sty, name, item), width, "…")
			})
		}
	}
	return rows
}

func workflowRunItemName(command string, item workflowhost.RunItem) string {
	switch item.Kind {
	case workflowhost.RunItemAgent:
		return workflowhost.AgentSessionTitle(command, item.Agent)
	case workflowhost.RunItemSession:
		if item.Title != "" {
			return item.Title
		}
		return "/" + command
	default:
		return ""
	}
}

func workflowRunItemLine(sty *styles.Styles, name string, item workflowhost.RunItem) string {
	sep := sty.Tool.StatsSep.Render(" · ")
	switch item.Kind {
	case workflowhost.RunItemAsk:
		answer := firstLine(item.Answer)
		switch item.Status {
		case workflowhost.StatusRunning:
			answer = "waiting for you"
		case workflowhost.StatusCompleted:
		default:
			answer = "unanswered (" + string(item.Status) + ")"
		}
		return sty.Tool.IconPending.UnsetString().Render("?") + " " + sty.Tool.NameNormal.Render(firstLine(item.Question)) +
			sty.Tool.StatsSep.Render(" → ") + sty.Tool.StatsLine.Render(answer)
	case workflowhost.RunItemSession:
		return sty.Tool.IconSuccess.UnsetString().Render("◆") + " " + sty.Tool.NameNormal.Render(name) + sep + sty.Tool.StatsLine.Render("session")
	default:
		label := sty.Tool.NameNormal.Render(name)
		if item.Model != "" {
			label += " " + sty.Tool.StatsLine.Render(item.Model)
		}
		state := string(item.Status)
		switch item.Status {
		case workflowhost.StatusCompleted:
			state = "done"
		case workflowhost.StatusFailed:
			state = "failed: " + firstLine(item.Err)
		}
		return workflowStatusIcon(sty, item.Status) + " " + label + sep + sty.Tool.StatsLine.Render(state)
	}
}

// outcomeDetail is what a finished run said about how it ended: its result
// when it completed, its error otherwise.
func outcomeDetail(view workflowhost.RunView) string {
	if view.Status != workflowhost.StatusCompleted {
		return firstLine(view.Error)
	}
	var buf bytes.Buffer
	if len(view.Result) == 0 || json.Compact(&buf, view.Result) != nil || buf.String() == "null" {
		return ""
	}
	return buf.String()
}

// runningState says how a running run is going.
func runningState(view workflowhost.RunView) string {
	if view.WaitingForUser() {
		return "waiting for you"
	}
	return "running"
}

func workflowStatusIcon(sty *styles.Styles, status workflowhost.Status) string {
	switch status {
	case workflowhost.StatusRunning:
		return sty.Tool.IconPending.String()
	case workflowhost.StatusCompleted:
		return sty.Tool.IconSuccess.String()
	case workflowhost.StatusFailed:
		return sty.Tool.IconError.String()
	default:
		return sty.Tool.IconCancelled.String()
	}
}

func formatRunDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// WorkflowOutcomeText describes how a run ended, given its result for a
// completed run or the host's message otherwise. It is the one place a
// Status becomes words, so every view of a run says the same thing.
func WorkflowOutcomeText(status workflowhost.Status, text string) string {
	switch status {
	case workflowhost.StatusRunning:
		return "running"
	case workflowhost.StatusCompleted:
		if text == "" {
			return "completed"
		}
		return "completed: " + text
	case workflowhost.StatusDenied:
		return "not approved"
	case workflowhost.StatusAborted:
		return "aborted"
	case workflowhost.StatusInterrupted:
		// The host's message already says the run was interrupted, and why.
		if text == "" {
			return "interrupted"
		}
		return text
	case workflowhost.StatusFailed:
		return "failed: " + text
	default:
		return string(status)
	}
}

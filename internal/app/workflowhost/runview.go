package workflowhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// RunView is what one workflow run looks like to the user: where it's up to
// and how to reach every session it created. It's rebuilt from the database
// alone, so it's the same after a restart.
type RunView struct {
	RunID             string
	InvokingSessionID string
	// Command is the name the run was started as, without the slash.
	Command string
	Input   string
	Status  Status
	// Paused reports whether the user paused the running run.
	Paused bool
	// Error says why a run that didn't complete ended.
	Error      string
	Result     json.RawMessage
	StartedAt  time.Time
	FinishedAt time.Time // Zero while running.
	// Cost is what every session the run made has cost so far. Builder
	// sessions keep their own cost rather than adding to the invoking
	// session's, so this is the one place a run's total shows.
	Cost float64
	// Phases are the run's status() labels in order, each with what
	// happened under it. Anything before the first status() is in a phase
	// with no step.
	Phases []RunPhase
}

// RunPhase is a status() label and what happened under it.
type RunPhase struct {
	Step, Detail string
	Items        []RunItem
}

// RunItemKind says what a RunItem is.
type RunItemKind string

const (
	// RunItemSession is an orchestrator session the run created.
	RunItemSession RunItemKind = "session"
	// RunItemAgent is a subagent the run ran.
	RunItemAgent RunItemKind = "agent"
	// RunItemAsk is a question the run asked the user.
	RunItemAsk RunItemKind = "ask"
)

// RunItem is something in a run worth showing on its own: a session it
// created, a subagent, or a question to the user.
type RunItem struct {
	Kind RunItemKind
	// SessionID is the session to open, empty for a question or a subagent
	// that failed before it had one.
	SessionID string
	// Title is a session's title.
	Title string
	// Agent and Model name a subagent; Model is empty unless the script
	// pinned one.
	Agent, Model string
	// Status is running until the call ends, then completed or failed with
	// Err saying why.
	Status Status
	Err    string
	// Question and Answer are a question's; Answer is empty while it's
	// waiting.
	Question, Answer string
}

// Current returns the run's latest status() label: its step and detail.
func (v RunView) Current() (string, string) {
	if len(v.Phases) == 0 {
		return "", ""
	}
	last := v.Phases[len(v.Phases)-1]
	return last.Step, last.Detail
}

// WaitingForUser reports whether a running run is waiting on a question
// to the user.
func (v RunView) WaitingForUser() bool {
	if v.Status != StatusRunning {
		return false
	}
	for _, p := range v.Phases {
		for _, item := range p.Items {
			if item.Kind == RunItemAsk && item.Status == StatusRunning {
				return true
			}
		}
	}
	return false
}

// RunView loads the view of a run, reporting false when there's no such
// run.
func (s *Service) RunView(ctx context.Context, runID string) (RunView, bool, error) {
	rec, ok, err := s.deps.Store.GetRun(ctx, runID)
	if err != nil || !ok {
		return RunView{}, ok, err
	}
	events, err := s.deps.Store.listViewEvents(ctx, runID)
	if err != nil {
		return RunView{}, false, err
	}
	sessions, err := s.deps.Sessions.ListByWorkflowRun(ctx, runID)
	if err != nil {
		return RunView{}, false, err
	}
	view := buildRunView(rec, events, sessions)
	// The run row records the command's ID; the name it was started as is
	// on its marker.
	if rec.MarkerMessageID != "" {
		marker, err := s.deps.Messages.Get(ctx, rec.MarkerMessageID)
		if err != nil {
			return RunView{}, false, fmt.Errorf("read marker of workflow run %s: %w", runID, err)
		}
		for _, part := range marker.Parts {
			if c, ok := part.(message.WorkflowRunContent); ok {
				view.Command = c.Command
			}
		}
	}
	return view, true, nil
}

// buildRunView replays a run's events into its view. Events arrive ordered
// by seq, and a call's seq is taken when it starts, so this is the order
// things started in.
func buildRunView(rec RunRecord, events []workflow.Event, sessions []session.Session) RunView {
	b := viewBuilder{
		v: RunView{
			RunID:             rec.ID,
			InvokingSessionID: rec.InvokingSessionID,
			Input:             rec.Input,
			Status:            rec.Status,
			Paused:            rec.Paused && rec.Status == StatusRunning,
			Error:             rec.Error,
			Result:            rec.Result,
			StartedAt:         rec.StartedAt,
			FinishedAt:        rec.FinishedAt,
		},
		bySeq: make(map[int]session.Session, len(sessions)),
		at:    map[int]itemPlace{},
	}
	for _, s := range sessions {
		b.bySeq[s.WorkflowSeq] = s
		b.v.Cost += s.Cost
	}
	for _, ev := range events {
		b.apply(ev)
	}
	b.endOpenCalls()
	return b.v
}

// viewBuilder accumulates a RunView from a run's events.
type viewBuilder struct {
	v RunView
	// bySeq finds the session each event created.
	bySeq map[int]session.Session
	// at locates each item by the seq of the call that made it, so the
	// call's outcome can update it.
	at map[int]itemPlace
}

type itemPlace struct{ phase, item int }

// startHandlers turn the start of each call worth showing into the view.
// A call's arguments are as the script passed them, so they're read
// leniently: a script bug shows as an odd row, never as a view that can't
// load, which would leave every session the run made unreachable.
var startHandlers = map[string]func(b *viewBuilder, ev workflow.Event){
	"status": func(b *viewBuilder, ev workflow.Event) {
		args := fields(ev.Args)
		b.v.Phases = append(b.v.Phases, RunPhase{Step: args.text("step"), Detail: args.text("detail")})
	},
	"session.new":  (*viewBuilder).addSession,
	"session.fork": (*viewBuilder).addSession,
	"agent": func(b *viewBuilder, ev workflow.Event) {
		args := fields(ev.Args)
		s := b.bySeq[ev.Seq]
		b.add(ev.Seq, RunItem{Kind: RunItemAgent, SessionID: s.ID, Title: s.Title, Agent: args.text("agent"), Model: args.text("model"), Status: StatusRunning})
	},
	"ask": func(b *viewBuilder, ev workflow.Event) {
		b.add(ev.Seq, RunItem{Kind: RunItemAsk, Question: fields(ev.Args).text("question"), Status: StatusRunning})
	},
}

// eventFields is a recorded JSON object's fields.
type eventFields map[string]json.RawMessage

// fields reads raw as a JSON object, empty when it isn't one.
func fields(raw json.RawMessage) eventFields {
	var f eventFields
	_ = json.Unmarshal(raw, &f) // Not an object: no fields.
	return f
}

// text is a field as text: a string's value, or other JSON as written.
func (f eventFields) text(name string) string {
	return jsonText(f[name])
}

// jsonText is raw as text: a string's value, other JSON compacted, or ""
// for none.
func jsonText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// A null decodes as a string above, to "".
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return ""
	}
	return buf.String()
}

// addSession shows an orchestrator session the event created.
func (b *viewBuilder) addSession(ev workflow.Event) {
	s := b.bySeq[ev.Seq]
	b.add(ev.Seq, RunItem{Kind: RunItemSession, SessionID: s.ID, Title: s.Title, Status: StatusRunning})
}

func (b *viewBuilder) apply(ev workflow.Event) {
	if ev.Phase != workflow.PhaseStarted {
		if p, ok := b.at[ev.Seq]; ok {
			settle(&b.v.Phases[p.phase].Items[p.item], ev)
		}
		return
	}
	if start, ok := startHandlers[ev.Primitive]; ok {
		start(b, ev)
	}
}

// add puts item in the current phase, starting one if the run hasn't
// called status() yet.
func (b *viewBuilder) add(seq int, item RunItem) {
	if len(b.v.Phases) == 0 {
		b.v.Phases = append(b.v.Phases, RunPhase{})
	}
	p := len(b.v.Phases) - 1
	b.v.Phases[p].Items = append(b.v.Phases[p].Items, item)
	b.at[seq] = itemPlace{p, len(b.v.Phases[p].Items) - 1}
}

// endOpenCalls ends calls a finished run left open (it was interrupted, or
// aborted mid-call) the way the run ended, so they don't read as running
// forever.
func (b *viewBuilder) endOpenCalls() {
	if b.v.Status == StatusRunning {
		return
	}
	for _, p := range b.at {
		if item := &b.v.Phases[p.phase].Items[p.item]; item.Status == StatusRunning {
			item.Status = b.v.Status
		}
	}
}

// settle applies a call's outcome to its item.
func settle(item *RunItem, ev workflow.Event) {
	if ev.Phase == workflow.PhaseFailed {
		item.Status, item.Err = StatusFailed, fields(ev.Error).text("message")
		return
	}
	item.Status = StatusCompleted
	if item.Kind == RunItemAsk {
		item.Answer = jsonText(ev.Result)
	}
}

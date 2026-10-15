package workflowhost

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// RunView is what one workflow run looks like to the user: where it's up to
// and how to reach every session it created. It's rebuilt from the database
// alone, so it's the same after a restart.
type RunView struct {
	RunID             string
	InvokingSessionID string
	MarkerMessageID   string
	Input             string
	Status            Status
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

// Current returns the run's latest status() label.
func (v RunView) Current() (step, detail string) {
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
	events, err := s.deps.Store.ListEvents(ctx, runID)
	if err != nil {
		return RunView{}, false, err
	}
	sessions, err := s.deps.Sessions.ListByWorkflowRun(ctx, runID)
	if err != nil {
		return RunView{}, false, err
	}
	return buildRunView(rec, events, sessions)
}

// buildRunView replays a run's events into its view. Events arrive ordered
// by seq, and a call's seq is taken when it starts, so this is the order
// things started in.
func buildRunView(rec RunRecord, events []workflow.Event, sessions []session.Session) (RunView, bool, error) {
	b := viewBuilder{
		v: RunView{
			RunID:             rec.ID,
			InvokingSessionID: rec.InvokingSessionID,
			MarkerMessageID:   rec.MarkerMessageID,
			Input:             rec.Input,
			Status:            rec.Status,
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
		if err := b.apply(ev); err != nil {
			return RunView{}, false, err
		}
	}
	b.endOpenCalls()
	return b.v, true, nil
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
var startHandlers = map[string]func(b *viewBuilder, ev workflow.Event) error{
	"status": func(b *viewBuilder, ev workflow.Event) error {
		var args struct{ Step, Detail string }
		if err := json.Unmarshal(ev.Args, &args); err != nil {
			return fmt.Errorf("read status event %d: %w", ev.Seq, err)
		}
		b.v.Phases = append(b.v.Phases, RunPhase{Step: args.Step, Detail: args.Detail})
		return nil
	},
	"session.new":  (*viewBuilder).addSession,
	"session.fork": (*viewBuilder).addSession,
	"agent": func(b *viewBuilder, ev workflow.Event) error {
		var args struct{ Agent, Model string }
		if err := json.Unmarshal(ev.Args, &args); err != nil {
			return fmt.Errorf("read agent event %d: %w", ev.Seq, err)
		}
		s := b.bySeq[ev.Seq]
		b.add(ev.Seq, RunItem{Kind: RunItemAgent, SessionID: s.ID, Title: s.Title, Agent: args.Agent, Model: args.Model, Status: StatusRunning})
		return nil
	},
	"ask": func(b *viewBuilder, ev workflow.Event) error {
		var args struct{ Question string }
		if err := json.Unmarshal(ev.Args, &args); err != nil {
			return fmt.Errorf("read ask event %d: %w", ev.Seq, err)
		}
		b.add(ev.Seq, RunItem{Kind: RunItemAsk, Question: args.Question, Status: StatusRunning})
		return nil
	},
}

// addSession shows an orchestrator session the event created.
func (b *viewBuilder) addSession(ev workflow.Event) error {
	s := b.bySeq[ev.Seq]
	b.add(ev.Seq, RunItem{Kind: RunItemSession, SessionID: s.ID, Title: s.Title, Status: StatusRunning})
	return nil
}

func (b *viewBuilder) apply(ev workflow.Event) error {
	if ev.Phase != workflow.PhaseStarted {
		if p, ok := b.at[ev.Seq]; ok {
			return settle(&b.v.Phases[p.phase].Items[p.item], ev)
		}
		return nil
	}
	if start, ok := startHandlers[ev.Primitive]; ok {
		return start(b, ev)
	}
	return nil
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
func settle(item *RunItem, ev workflow.Event) error {
	if ev.Phase == workflow.PhaseFailed {
		var info struct{ Message string }
		_ = json.Unmarshal(ev.Error, &info)
		item.Status, item.Err = StatusFailed, info.Message
		return nil
	}
	item.Status = StatusCompleted
	if item.Kind == RunItemAsk {
		if err := json.Unmarshal(ev.Result, &item.Answer); err != nil {
			return fmt.Errorf("read answer of ask event %d: %w", ev.Seq, err)
		}
	}
	return nil
}

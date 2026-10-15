package workflowhost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
	"github.com/Broderick-Westrope/anvil/internal/wtp"
	"mvdan.cc/sh/v3/interp"
)

// catalog answers requirement checks from the coordinator's current skills
// and agents.
type catalog struct{ coord agent.Coordinator }

func (c catalog) HasSkill(name string) bool { return c.coord.ActiveSkillByName(name) != nil }
func (c catalog) HasAgent(name string) bool { return c.coord.HasAgent(name) }

// backend implements workflow.Backend for one run.
type backend struct {
	svc  *Service
	base Event
}

var _ workflow.Backend = (*backend)(nil)

func (b *backend) HasSkill(name string) bool { return b.svc.Catalog().HasSkill(name) }
func (b *backend) HasAgent(name string) bool { return b.svc.Catalog().HasAgent(name) }

func (b *backend) publish(ev Event) {
	ev.RunID, ev.Command, ev.SessionID = b.base.RunID, b.base.Command, b.base.SessionID
	b.svc.events.Publish(pubsub.UpdatedEvent, ev)
}

// publishState publishes an event the UI keeps as state, which a dropped
// event would leave stale.
func (b *backend) publishState(ctx context.Context, ev Event) {
	ev.RunID, ev.Command, ev.SessionID = b.base.RunID, b.base.Command, b.base.SessionID
	b.svc.events.PublishMustDeliver(context.WithoutCancel(ctx), pubsub.UpdatedEvent, ev)
}

func (b *backend) Skill(name string) (workflow.SkillContent, bool) {
	skill := b.svc.deps.Coordinator.ActiveSkillByName(name)
	if skill == nil {
		return workflow.SkillContent{}, false
	}
	return workflow.SkillContent{Body: skill.Instructions, Location: skill.SkillFilePath}, true
}

// withSkills preloads skills into a prompt the same way a command's
// skills: frontmatter does.
func (b *backend) withSkills(names []string, prompt string) string {
	resolved := skills.ResolveContent(names, b.svc.deps.Coordinator.ActiveSkillByName)
	if resolved == "" {
		return prompt
	}
	return resolved + "\n\n" + prompt
}

func (b *backend) Turn(ctx context.Context, call workflow.TurnCall) (string, error) {
	b.publish(Event{Kind: EventStep, Activity: &Activity{Kind: ActivityTurn, Session: call.SessionID}})
	prompt := commands.FormatWorkflowTurnXML(b.base.Command, b.withSkills(call.Skills, call.Prompt))
	result, err := b.svc.deps.Coordinator.RunTurn(ctx, call.SessionID, prompt)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return result.Response.Content.Text(), nil
}

func (b *backend) NewSession(ctx context.Context, call workflow.SessionCall) (string, error) {
	return b.ownedSession(ctx, call, func(string) (string, error) { return "", nil })
}

// Fork makes a session for the run whose history is a copy of the
// source's. The invoking session is copied up to the run's marker, so
// every fork has the conversation the user had before starting the run,
// whatever they've said there since. One of the run's own sessions is
// copied as it is once its work in flight settles.
func (b *backend) Fork(ctx context.Context, call workflow.ForkCall) (string, error) {
	return b.ownedSession(ctx, call.SessionCall, func(forkID string) (string, error) {
		if call.FromInvoker {
			leaf, err := b.startBoundary(ctx)
			if err != nil {
				return "", err
			}
			_, end, err := b.svc.deps.Messages.CopyBranch(ctx, leaf, forkID)
			return end, err
		}
		var end string
		err := b.svc.deps.Coordinator.RunInSession(ctx, call.Source, func(ctx context.Context) error {
			source, err := b.svc.deps.Sessions.Get(ctx, call.Source)
			if err != nil {
				return err
			}
			_, end, err = b.svc.deps.Messages.CopyBranch(ctx, source.LeafMessageID, forkID)
			return err
		})
		return end, err
	})
}

// startBoundary is the last message of the invoking session before the
// run's marker.
func (b *backend) startBoundary(ctx context.Context) (string, error) {
	run, ok, err := b.svc.deps.Store.GetRun(ctx, b.base.RunID)
	if err != nil {
		return "", err
	}
	if !ok || run.MarkerMessageID == "" {
		return "", fmt.Errorf("workflow run %s has no marker to fork from", b.base.RunID)
	}
	marker, err := b.svc.deps.Messages.Get(ctx, run.MarkerMessageID)
	if err != nil {
		return "", fmt.Errorf("read marker of workflow run %s: %w", b.base.RunID, err)
	}
	return marker.ParentMessageID, nil
}

// ownedSession creates an orchestrator session the run owns and lets fill
// give it history, returning the source message the history ends at, if
// any. It's a child of the session that started the run, so it stays out of
// the session list and is reached through the run's node. It's recorded as
// the run's before anything goes into it, and deleted if it can't be
// finished, so a failure never leaves a copy of a conversation nobody can
// reach.
func (b *backend) ownedSession(ctx context.Context, call workflow.SessionCall, fill func(sessionID string) (string, error)) (id string, err error) {
	// A title the script chose is kept; without one, the session starts as
	// "/cmd" and is auto-titled after its first turn like any session.
	title := call.Title
	chosen := title != ""
	if !chosen {
		title = "/" + b.base.Command
	}
	s, err := b.svc.deps.Sessions.CreateTaskSession(ctx, uuid.NewString(), b.base.SessionID, title)
	if err != nil {
		return "", err
	}
	defer func() {
		if err == nil {
			return
		}
		if derr := b.svc.deps.Sessions.Delete(context.WithoutCancel(ctx), s.ID); derr != nil {
			slog.Warn("Failed to remove an unfinished workflow session", "session", s.ID, "error", derr)
		}
	}()
	owner := session.WorkflowOwner{RunID: b.base.RunID, Seq: call.Seq, Kind: session.WorkflowKindSession}
	if err := b.svc.deps.Sessions.SetWorkflowOwner(ctx, s.ID, owner); err != nil {
		return "", err
	}
	if chosen {
		if err := b.svc.deps.Sessions.Rename(ctx, s.ID, title, true); err != nil {
			return "", fmt.Errorf("keep session title: %w", err)
		}
	}
	if owner.ForkedFrom, err = fill(s.ID); err != nil {
		return "", fmt.Errorf("copy history into session: %w", err)
	}
	if owner.ForkedFrom != "" {
		if err := b.svc.deps.Sessions.SetWorkflowOwner(ctx, s.ID, owner); err != nil {
			return "", err
		}
	}
	return s.ID, nil
}

// AgentSessionTitle titles the session a workflow's subagent runs in, which
// is also how every view names that subagent.
func AgentSessionTitle(command, agent string) string {
	return "/" + command + ": " + agent
}

// Agent runs a subagent in a child session whose ID is published once the
// session exists, so the UI can open it while it runs. Its messages stay
// in that session and never reach the invoking one. An agent that fails
// before its session exists is published only as failed, with no session.
func (b *backend) Agent(ctx context.Context, call workflow.AgentCall) (workflow.AgentResult, error) {
	wc := agent.WorkflowAgentCall{
		ParentSessionID: call.ParentSessionID,
		Agent:           call.Agent,
		Prompt:          b.withSkills(call.Skills, call.Prompt),
		Title:           AgentSessionTitle(b.base.Command, call.Agent),
		Tools:           call.Tools,
		Model:           call.Model,
	}
	if call.Schema != nil {
		schema, err := workflow.CompileSchema(call.Schema)
		if err != nil {
			return workflow.AgentResult{}, err
		}
		wc.Schema, wc.Validate = call.Schema, schema.Validate
	}
	ended := AgentCall{Name: call.Agent, Model: call.Model, Status: StatusCompleted}
	// A subagent whose session can't be tied to the run would be
	// unreachable from it, so failing to record that stops the subagent.
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	wc.OnSession = func(sessionID string) {
		// The run view finds the session by the event that made it, so
		// it's recorded before anyone hears the subagent started.
		if err := b.svc.deps.Sessions.SetWorkflowOwner(ctx, sessionID, session.WorkflowOwner{
			RunID: b.base.RunID, Seq: call.Seq, Kind: session.WorkflowKindAgent,
		}); err != nil {
			stop(fmt.Errorf("record subagent session: %w", err))
			ended.Session = sessionID
			return
		}
		started := ended
		started.Session, started.Status = sessionID, StatusRunning
		b.publishState(ctx, Event{Kind: EventAgent, Agent: &started})
		ended.Session = sessionID
	}
	res, err := b.runAgent(ctx, wc)
	if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, context.Canceled) {
		err = cause
	}
	if err != nil {
		ended.Status, ended.Err = StatusFailed, err.Error()
	}
	b.publishState(ctx, Event{Kind: EventAgent, Agent: &ended})
	return res, err
}

func (b *backend) runAgent(ctx context.Context, wc agent.WorkflowAgentCall) (workflow.AgentResult, error) {
	out, err := b.svc.deps.Coordinator.RunWorkflowAgent(ctx, wc)
	if err != nil {
		return workflow.AgentResult{}, err
	}
	if wc.Schema != nil && out.Output == nil {
		return workflow.AgentResult{}, &workflow.SchemaError{
			Raw: out.Text, Reason: "the agent never called " + agent.SubmitResultToolName + " with a valid result",
		}
	}
	return workflow.AgentResult{Text: out.Text, Output: out.Output}, nil
}

func (b *backend) Bash(ctx context.Context, call workflow.BashCall) (workflow.BashResult, error) {
	cwd := call.Cwd
	if cwd == "" {
		cwd = b.svc.deps.WorkingDir
	}
	b.publish(Event{Kind: EventStep, Activity: &Activity{Kind: ActivityBash, Cmd: call.Cmd}})
	sh := shell.NewShell(&shell.Options{WorkingDir: cwd})
	// wtp runs as a child process so cancelling a workflow kills hooks
	// it started too; in-process it can't stop them.
	stdout, stderr, err := sh.Exec(shell.WithExternalWTP(ctx, wtp.Command()), call.Cmd)
	if err != nil && shell.IsInterrupt(err) {
		return workflow.BashResult{}, err
	}
	res := workflow.BashResult{Stdout: stdout, Stderr: stderr, ExitCode: shell.ExitCode(err)}
	if _, isExit := errors.AsType[interp.ExitStatus](err); err != nil && !isExit {
		// The command never ran to an exit status, e.g. it didn't parse.
		res.Stderr += err.Error()
	}
	return res, nil
}

func (b *backend) Ask(ctx context.Context, question string, options []string) (string, error) {
	return b.svc.request(ctx, Event{
		Kind: EventAsk, RunID: b.base.RunID, Command: b.base.Command, SessionID: b.base.SessionID,
		Question: question, Options: options,
	})
}

func (b *backend) Status(step, detail string) {
	b.publish(Event{Kind: EventStatus, Progress: &Progress{Step: step, Detail: detail}})
}

func (b *backend) Warn(msg string) {
	b.publish(Event{Kind: EventWarn, Message: msg})
}

func (b *backend) Hold(ctx context.Context, call workflow.HoldCall) (func(), error) {
	release, err := b.svc.deps.Coordinator.Hold(ctx, call.SessionIDs)
	if err != nil {
		return nil, err
	}
	// The hold has waited out in-flight runs, so subagents have handed
	// their surviving jobs to these sessions; what's left can still write
	// to the workspace the script is about to change.
	if err := settleJobs(call.SessionIDs, call.StopJobs); err != nil {
		release()
		return nil, err
	}
	b.publishState(ctx, Event{Kind: EventState, State: StateHeld})
	return func() {
		release()
		b.publishState(ctx, Event{Kind: EventState, State: StateReleased})
	}, nil
}

// settleJobs refuses with *workflow.JobsRunningError while the sessions own
// running background jobs, or kills them when stop is set.
func settleJobs(sessionIDs []string, stop bool) error {
	mgr := shell.GetBackgroundShellManager()
	var running []workflow.Job
	for _, id := range sessionIDs {
		for _, job := range mgr.ListBySession(id) {
			if !job.Done {
				running = append(running, workflow.Job{ID: job.ID, Command: job.Command, SessionID: job.SessionID})
			}
		}
	}
	if len(running) == 0 {
		return nil
	}
	if !stop {
		return &workflow.JobsRunningError{Jobs: running}
	}
	var errs []error
	for _, job := range running {
		if err := mgr.Kill(job.ID); err != nil {
			errs = append(errs, fmt.Errorf("stop job %s: %w", job.ID, err))
		}
	}
	return errors.Join(errs...)
}

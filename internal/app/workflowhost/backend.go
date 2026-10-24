package workflowhost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

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

func (b *backend) HasSkill(name string) bool { return b.svc.catalog().HasSkill(name) }
func (b *backend) HasAgent(name string) bool { return b.svc.catalog().HasAgent(name) }

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
// source's. The invoking session is copied up to the run's marker, its
// start boundary, so every fork has the conversation the user had before
// starting the run, whatever they've said there since. One of the run's own
// sessions is copied as it is once its work in flight settles.
func (b *backend) Fork(ctx context.Context, call workflow.ForkCall) (string, error) {
	return b.ownedSession(ctx, call.SessionCall, func(forkID string) (string, error) {
		if call.FromInvoker {
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
			_, end, err := b.svc.deps.Messages.CopyBranch(ctx, marker.ParentMessageID, forkID)
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

// ownedSession creates an orchestrator session the run owns and lets fill
// give it history, returning the source message the history ends at, if
// any. It's a child of the session that started the run, so it stays out of
// the session list and is reached through the run's node. It's created as
// the run's, and deleted if it can't be finished, so a failure never leaves
// a copy of a conversation nobody can reach.
func (b *backend) ownedSession(ctx context.Context, call workflow.SessionCall, fill func(sessionID string) (string, error)) (string, error) {
	// A title the script chose is kept; without one, the session starts as
	// "/cmd" and is auto-titled after its first turn like any session.
	title := call.Title
	chosen := title != ""
	if !chosen {
		title = "/" + b.base.Command
	}
	owner := session.WorkflowOwner{RunID: b.base.RunID, Seq: call.Seq, Kind: session.WorkflowKindSession}
	s, err := b.svc.deps.Sessions.CreateWorkflowSession(ctx, "", b.base.SessionID, title, owner)
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		if derr := b.svc.deps.Sessions.Discard(context.WithoutCancel(ctx), s.ID); derr != nil {
			slog.Warn("Failed to remove an unfinished workflow session", "session", s.ID, "error", derr)
		}
		return "", err
	}
	if chosen {
		if err := b.svc.deps.Sessions.Rename(ctx, s.ID, title, true); err != nil {
			return fail(fmt.Errorf("keep session title: %w", err))
		}
	}
	forkedFrom, err := fill(s.ID)
	if err != nil {
		return fail(fmt.Errorf("copy history into session: %w", err))
	}
	if forkedFrom != "" {
		owner.ForkedFrom = forkedFrom
		if err := b.svc.deps.Sessions.SetWorkflowOwner(ctx, s.ID, owner); err != nil {
			return fail(err)
		}
	}
	return s.ID, nil
}

// AgentSessionTitle titles the session a workflow's subagent runs in, which
// is also how every view names that subagent.
func AgentSessionTitle(command, agent string) string {
	return "/" + command + ": " + agent
}

// Agent runs a subagent in a child session, recorded as the run's once it
// exists so the run view can open it while it runs. Its messages stay in
// that session and never reach the invoking one.
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
	// The session is created as the run's, so the run view can open it
	// from the start; the view finds it through that owner, which the
	// call's started event came before, so it's told to look again.
	wc.Owner = &session.WorkflowOwner{RunID: b.base.RunID, Seq: call.Seq, Kind: session.WorkflowKindAgent}
	wc.OnSession = func(string) {
		b.publishState(ctx, Event{Kind: EventChanged})
	}
	return b.runAgent(ctx, wc)
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
	sh := shell.NewShell(&shell.Options{WorkingDir: cwd})
	// wtp runs as a child process so cancelling a workflow kills hooks
	// it started too; in-process it can't stop them.
	stdout, stderr, err := sh.Exec(shell.WithExternalWTP(ctx, wtp.Command()), call.Cmd)
	// An aborted run's command was interrupted, however the shell reports
	// it: aborted as it starts, a command can stop with an error that isn't
	// an interrupt, which would read as a command that ran and exited.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return workflow.BashResult{}, ctxErr
	}
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

func (b *backend) Warn(msg string) {
	b.publish(Event{Kind: EventWarn, Message: msg})
}

func (b *backend) Hold(ctx context.Context, call workflow.HoldCall) (func(), error) {
	release, err := b.svc.deps.Coordinator.Hold(ctx, call.SessionIDs)
	if err != nil {
		return nil, err
	}
	// The hold has waited out in-flight runs, so subagents have finished.
	// Their surviving jobs stay in their own sessions, and with the held
	// sessions' own jobs can still write to the workspace the script is
	// about to change.
	owned, err := b.svc.deps.Sessions.ListByWorkflowRun(ctx, b.base.RunID)
	if err != nil {
		release()
		return nil, err
	}
	jobSessions := slices.Clone(call.SessionIDs)
	for _, s := range owned {
		if s.WorkflowKind == session.WorkflowKindAgent {
			jobSessions = append(jobSessions, s.ID)
		}
	}
	if err := settleJobs(jobSessions, call.StopJobs); err != nil {
		release()
		return nil, err
	}
	return release, nil
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

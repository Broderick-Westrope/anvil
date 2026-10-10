package host

import (
	"context"
	"errors"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
	"mvdan.cc/sh/v3/interp"
)

// catalog answers requirement checks from the coordinator's current skills
// and agents.
type catalog struct{ coord agent.Coordinator }

func (c catalog) HasSkill(name string) bool { return c.coord.ActiveSkillByName(name) != nil }
func (c catalog) HasAgent(name string) bool { return c.coord.HasAgent(name) }

// backend implements workflow.Backend for one run.
type backend struct {
	catalog
	svc  *Service
	base Event
}

var _ workflow.Backend = (*backend)(nil)

func (b *backend) publish(ev Event) {
	ev.RunID, ev.Command, ev.SessionID = b.base.RunID, b.base.Command, b.base.SessionID
	b.svc.events.Publish(pubsub.UpdatedEvent, ev)
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
	b.publish(Event{Kind: EventStep, Step: "turn", Target: call.SessionID})
	prompt := commands.FormatWorkflowTurnXML(b.base.Command, b.withSkills(call.Skills, call.Prompt))
	result, err := b.coord.RunTurn(ctx, call.SessionID, prompt)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return result.Response.Content.Text(), nil
}

func (b *backend) NewSession(ctx context.Context, title string) (string, error) {
	if title == "" {
		title = "/" + b.base.Command
	}
	s, err := b.svc.deps.Sessions.Create(ctx, title, b.svc.deps.WorkingDir)
	if err != nil {
		return "", err
	}
	return s.ID, nil
}

func (b *backend) Agent(ctx context.Context, call workflow.AgentCall) (workflow.AgentResult, error) {
	wc := agent.WorkflowAgentCall{
		ParentSessionID: call.ParentSessionID,
		Agent:           call.Agent,
		Prompt:          b.withSkills(call.Skills, call.Prompt),
		Title:           "/" + b.base.Command + ": " + call.Agent,
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
	out, err := b.svc.deps.Coordinator.RunWorkflowAgent(ctx, wc)
	if err != nil {
		return workflow.AgentResult{}, err
	}
	if call.Schema != nil && out.Output == nil {
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
	b.publish(Event{Kind: EventStep, Step: "bash", Detail: call.Cmd})
	sh := shell.NewShell(&shell.Options{WorkingDir: cwd})
	stdout, stderr, err := sh.Exec(ctx, call.Cmd)
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
	b.publish(Event{Kind: EventStatus, Step: step, Detail: detail})
}

func (b *backend) Focus(sessionID string) {
	b.publish(Event{Kind: EventFocus, Target: sessionID})
}

func (b *backend) Warn(msg string) {
	b.publish(Event{Kind: EventWarn, Message: msg})
}

func (b *backend) Hold(ctx context.Context, sessionIDs []string) (func(), error) {
	release, err := b.svc.deps.Coordinator.Hold(ctx, sessionIDs)
	if err != nil {
		return nil, err
	}
	b.publish(Event{Kind: EventState, Held: true})
	return func() {
		release()
		b.publish(Event{Kind: EventState, Held: false})
	}, nil
}

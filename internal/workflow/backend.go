package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Backend implements the primitives that reach outside the script: models,
// sessions, the shell and the UI. Anvil provides the real one; host tests
// provide a fake.
type Backend interface {
	Catalog
	Skill(name string) (SkillContent, bool)
	// Turn runs prompt as a turn in an existing session and returns that
	// turn's own final text once its run finishes.
	Turn(ctx context.Context, call TurnCall) (string, error)
	// NewSession creates an interactive orchestrator session for the run.
	NewSession(ctx context.Context, call SessionCall) (string, error)
	// Agent runs a fresh, isolated subagent. With a schema, it returns the
	// validated output or a *SchemaError.
	Agent(ctx context.Context, call AgentCall) (AgentResult, error)
	Bash(ctx context.Context, call BashCall) (BashResult, error)
	Ask(ctx context.Context, question string, options []string) (string, error)
	Status(step, detail string)
	Focus(sessionID string)
	// Warn shows a non-fatal problem, such as undeclared use of a skill.
	Warn(msg string)
	// Hold stops new runs from starting in the given sessions and waits for
	// in-flight ones to finish. Background jobs the sessions own could still
	// write to the workspace, so it fails with *JobsRunningError while any
	// run, unless StopJobs asks it to stop them first. The returned release
	// lifts the hold.
	Hold(ctx context.Context, call HoldCall) (release func(), err error)
}

// SkillContent is a resolved skill.
type SkillContent struct {
	Body     string `json:"body"`
	Location string `json:"location"`
}

// TurnCall is a turn sent to a session. Skills are preloaded into the turn
// the same way a command's skills: frontmatter is.
type TurnCall struct {
	SessionID string
	Prompt    string
	Skills    []string
}

// SessionCall creates a session. Seq is the run's event that creates it.
type SessionCall struct {
	Title string
	Seq   int
}

// AgentCall runs a subagent. Tools restricts its tool set (fail closed),
// Model pins a provider/model, and Schema makes it return structured output.
type AgentCall struct {
	// Seq is the run's event that runs the subagent.
	Seq             int
	ParentSessionID string
	Agent           string
	Prompt          string
	Schema          json.RawMessage
	Skills          []string
	Tools           []string
	Model           string
}

// AgentResult is a subagent's output: Text without a schema, Output with
// one.
type AgentResult struct {
	Text   string
	Output json.RawMessage
}

// BashCall runs a command in Anvil's embedded shell.
type BashCall struct {
	Cmd string
	Cwd string
}

// BashResult is a finished command.
type BashResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

// ErrAborted ends a run that was aborted.
var ErrAborted = errors.New("workflow aborted")

// SchemaError reports structured output that never matched its schema.
type SchemaError struct {
	Raw    string
	Reason string
}

func (e *SchemaError) Error() string { return "output doesn't match schema: " + e.Reason }

// BashError reports a command that exited non-zero.
type BashError struct {
	BashResult
	Cmd string
}

func (e *BashError) Error() string {
	msg := fmt.Sprintf("command exited %d: %s", e.ExitCode, e.Cmd)
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		// The tail of stderr is usually the reason; keep the message short.
		lines := strings.Split(stderr, "\n")
		msg += ": " + lines[len(lines)-1]
	}
	return msg
}

// HoldCall holds a run's sessions for workflow.exclusive.
type HoldCall struct {
	SessionIDs []string
	StopJobs   bool
}

// Job is a background job that blocks a hold.
type Job struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	SessionID string `json:"sessionId"`
}

// JobsRunningError refuses a hold while background jobs owned by the held
// sessions are still running.
type JobsRunningError struct {
	Jobs []Job
}

func (e *JobsRunningError) Error() string {
	return fmt.Sprintf("%d background job(s) still running in the workflow's sessions", len(e.Jobs))
}

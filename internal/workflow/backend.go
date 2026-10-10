package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	NewSession(ctx context.Context, title string) (string, error)
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
	// in-flight ones to finish. The returned release lifts the hold.
	Hold(ctx context.Context, sessionIDs []string) (release func(), err error)
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

// AgentCall runs a subagent. Tools restricts its tool set (fail closed),
// Model pins a provider/model, and Schema makes it return structured output.
type AgentCall struct {
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
	return fmt.Sprintf("command exited %d: %s", e.ExitCode, e.Cmd)
}

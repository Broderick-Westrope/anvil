package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
)

// primitive implements one JS-facing call. seq is the call's sequence
// number, for primitives that record child events.
type primitive func(r *Run, seq int, args json.RawMessage) (any, error)

var primitives = map[string]primitive{
	"bash":             primBash,
	"turn":             primTurn,
	"agent":            primAgent,
	"parallel":         primParallel,
	"ask":              primAsk,
	"status":           primStatus,
	"skill":            primSkill,
	"reference":        primReference,
	"readFile":         primReadFile,
	"now":              primNow,
	"random":           primRandom,
	"session.current":  primSessionCurrent,
	"session.new":      primSessionNew,
	"workflow.tmpdir":  primTmpdir,
	"workflow.dir":     primDir,
	"workflow.focus":   primFocus,
	"workflow.hold":    primHold,
	"workflow.release": primRelease,
}

func decode[T any](args json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(args, &v); err != nil {
		return v, fmt.Errorf("invalid arguments: %w", err)
	}
	return v, nil
}

func primBash(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Cmd   string `json:"cmd"`
		Cwd   string `json:"cwd"`
		Check *bool  `json:"check"`
	}](args)
	if err != nil {
		return nil, err
	}
	res, err := r.backend.Bash(r.ctx, BashCall{Cmd: in.Cmd, Cwd: in.Cwd})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 && (in.Check == nil || *in.Check) {
		return nil, &BashError{BashResult: res, Cmd: in.Cmd}
	}
	return res, nil
}

func primTurn(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Session string          `json:"session"`
		Prompt  string          `json:"prompt"`
		Schema  json.RawMessage `json:"schema"`
		Skills  []string        `json:"skills"`
	}](args)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(r.Sessions(), in.Session) {
		return nil, fmt.Errorf("session %q doesn't belong to this run", in.Session)
	}
	r.mu.Lock()
	held := r.release != nil
	r.mu.Unlock()
	if held {
		// The run's sessions are held, so the turn could never start.
		return nil, errors.New("turn() can't run inside workflow.exclusive")
	}
	if err := r.checkSkills(in.Skills); err != nil {
		return nil, err
	}
	var schema *Schema
	if in.Schema != nil {
		if schema, err = CompileSchema(in.Schema); err != nil {
			return nil, err
		}
	}

	call := TurnCall{SessionID: in.Session, Prompt: in.Prompt, Skills: in.Skills}
	r.mu.Lock()
	s, seeded := r.seeds[in.Session]
	r.mu.Unlock()
	if seeded {
		call.Prompt = s.prompt + "\n\n" + call.Prompt
		call.Skills = append(slices.Clone(s.skills), call.Skills...)
	}
	if schema != nil {
		call.Prompt += schemaInstructions(schema)
	}
	reply, err := r.backend.Turn(r.ctx, call)
	if err != nil {
		return nil, err
	}
	// The seed stays until a turn delivers it, so a failed first turn
	// doesn't lose it.
	if seeded {
		r.mu.Lock()
		delete(r.seeds, in.Session)
		r.mu.Unlock()
	}
	if schema == nil {
		return reply, nil
	}
	out, verr := validated(schema, reply)
	if verr == nil {
		return out, nil
	}
	// One visible corrective turn, then give up.
	reply, err = r.backend.Turn(r.ctx, TurnCall{SessionID: in.Session, Prompt: schemaCorrection(verr.Error())})
	if err != nil {
		return nil, err
	}
	out, verr = validated(schema, reply)
	if verr != nil {
		return nil, &SchemaError{Raw: reply, Reason: verr.Error()}
	}
	return out, nil
}

func validated(schema *Schema, reply string) (json.RawMessage, error) {
	doc := lastJSONBlock(reply)
	if err := schema.Validate(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

type agentArgs struct {
	Agent  string          `json:"agent"`
	Prompt string          `json:"prompt"`
	Schema json.RawMessage `json:"schema"`
	Skills []string        `json:"skills"`
	Tools  []string        `json:"tools"`
	Model  string          `json:"model"`
}

func primAgent(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[agentArgs](args)
	if err != nil {
		return nil, err
	}
	return r.runAgent(r.ctx, in)
}

func (r *Run) runAgent(ctx context.Context, in agentArgs) (any, error) {
	if !r.backend.HasAgent(in.Agent) {
		return nil, fmt.Errorf("agent %q not found", in.Agent)
	}
	r.warnUndeclared("agent", in.Agent, r.script.Requires.Agents)
	if err := r.checkSkills(in.Skills); err != nil {
		return nil, err
	}
	if in.Schema != nil {
		if _, err := CompileSchema(in.Schema); err != nil {
			return nil, err
		}
	}
	res, err := r.backend.Agent(ctx, AgentCall{
		ParentSessionID: r.opts.InvokingSessionID,
		Agent:           in.Agent,
		Prompt:          in.Prompt,
		Schema:          in.Schema,
		Skills:          in.Skills,
		Tools:           in.Tools,
		Model:           in.Model,
	})
	if err != nil {
		return nil, err
	}
	if in.Schema != nil {
		return res.Output, nil
	}
	return res.Text, nil
}

type settled struct {
	OK    bool       `json:"ok"`
	Value any        `json:"value,omitempty"`
	Error *errorInfo `json:"error,omitempty"`
}

func primParallel(r *Run, seq int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Calls []json.RawMessage `json:"calls"`
	}](args)
	if err != nil {
		return nil, err
	}
	results := make([]settled, len(in.Calls))
	var wg sync.WaitGroup
	for i, raw := range in.Calls {
		wg.Go(func() {
			value, err := r.call(r.ctx, seq, i, "agent", raw, func(int) (any, error) {
				desc, err := decode[agentArgs](raw)
				if err != nil {
					return nil, err
				}
				return r.runAgent(r.ctx, desc)
			})
			if err != nil {
				info := describeError(err, r.aborted.Load())
				results[i] = settled{Error: &info}
				return
			}
			results[i] = settled{OK: true, Value: value}
		})
	}
	wg.Wait()
	return results, nil
}

func primAsk(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}](args)
	if err != nil {
		return nil, err
	}
	return r.backend.Ask(r.ctx, in.Question, in.Options)
}

func primStatus(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Step   string `json:"step"`
		Detail string `json:"detail"`
	}](args)
	if err != nil {
		return nil, err
	}
	r.backend.Status(in.Step, in.Detail)
	return nil, nil
}

func primSkill(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Name string `json:"name"`
	}](args)
	if err != nil {
		return nil, err
	}
	content, ok := r.backend.Skill(in.Name)
	if !ok {
		return nil, fmt.Errorf("skill %q not found", in.Name)
	}
	r.warnUndeclared("skill", in.Name, r.script.Requires.Skills)
	return content, nil
}

func primReference(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Path string `json:"path"`
	}](args)
	if err != nil {
		return nil, err
	}
	key, err := referenceKey(in.Path)
	if err != nil {
		return nil, err
	}
	content, ok := r.opts.References[key]
	if !ok {
		return nil, fmt.Errorf("reference %q not found", in.Path)
	}
	r.warnUndeclared("reference", in.Path, r.script.Requires.References)
	return content, nil
}

func primReadFile(_ *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Path string `json:"path"`
	}](args)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func primNow(r *Run, _ int, _ json.RawMessage) (any, error) {
	return r.opts.Now().UnixMilli(), nil
}

func primRandom(r *Run, _ int, _ json.RawMessage) (any, error) {
	return r.opts.Rand(), nil
}

func primSessionCurrent(r *Run, _ int, _ json.RawMessage) (any, error) {
	return r.opts.InvokingSessionID, nil
}

func primSessionNew(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Title  string   `json:"title"`
		Seed   string   `json:"seed"`
		Skills []string `json:"skills"`
	}](args)
	if err != nil {
		return nil, err
	}
	if err := r.checkSkills(in.Skills); err != nil {
		return nil, err
	}
	id, err := r.backend.NewSession(r.ctx, in.Title)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, id)
	if in.Seed != "" || len(in.Skills) > 0 {
		r.seeds[id] = seed{prompt: in.Seed, skills: in.Skills}
	}
	return id, nil
}

func primTmpdir(r *Run, _ int, _ json.RawMessage) (any, error) {
	if r.opts.Tmpdir == "" {
		return nil, errors.New("run has no tmpdir")
	}
	if err := os.MkdirAll(r.opts.Tmpdir, 0o700); err != nil {
		return nil, err
	}
	return r.opts.Tmpdir, nil
}

// primDir returns the workflow's directory, so a script can point agents
// at files it bundles.
func primDir(r *Run, _ int, _ json.RawMessage) (any, error) {
	return r.script.Dir(), nil
}

func primFocus(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		Session string `json:"session"`
	}](args)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(r.Sessions(), in.Session) {
		return nil, fmt.Errorf("session %q doesn't belong to this run", in.Session)
	}
	r.backend.Focus(in.Session)
	return nil, nil
}

func primHold(r *Run, _ int, args json.RawMessage) (any, error) {
	in, err := decode[struct {
		StopJobs bool `json:"stopJobs"`
	}](args)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	held := r.release != nil
	r.mu.Unlock()
	if held {
		return nil, errors.New("workflow.exclusive can't be nested")
	}
	release, err := r.backend.Hold(r.ctx, HoldCall{SessionIDs: r.Sessions(), StopJobs: in.StopJobs})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.release = release
	r.mu.Unlock()
	return nil, nil
}

func primRelease(r *Run, _ int, _ json.RawMessage) (any, error) {
	r.releaseHold()
	return nil, nil
}

func (r *Run) checkSkills(names []string) error {
	for _, name := range names {
		if !r.backend.HasSkill(name) {
			return fmt.Errorf("skill %q not found", name)
		}
		r.warnUndeclared("skill", name, r.script.Requires.Skills)
	}
	return nil
}

// warnUndeclared reports use of an item missing from requires(), once per
// item.
func (r *Run) warnUndeclared(kind, name string, declared []string) {
	if slices.Contains(declared, name) {
		return
	}
	key := kind + ":" + name
	r.mu.Lock()
	seen := r.warned[key]
	r.warned[key] = true
	r.mu.Unlock()
	if !seen {
		r.backend.Warn(fmt.Sprintf("undeclared: add %s %q to requires()", kind, name))
	}
}

package workflow

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

//go:embed prelude.js
var prelude string

// Input is what main(input) receives.
type Input struct {
	Raw string `json:"raw"`
}

// Options configure a run.
type Options struct {
	// InvokingSessionID is the session the workflow was invoked from; it is
	// what session.current() returns.
	InvokingSessionID string
	// Tmpdir is the run's scratch directory, created on first use.
	Tmpdir string
	// References are the files reference() reads, by [BundledFile.Rel]:
	// the contents approved before the run, so an edit made during it,
	// or a file outside the bundle, is never read.
	References map[string]string
	Now        func() time.Time
	Rand       func() float64
}

// Run is one execution of a script. A Run executes once.
type Run struct {
	script  *Script
	backend Backend
	rec     Recorder
	opts    Options

	ctx      context.Context
	cancel   context.CancelFunc
	vm       *goja.Runtime
	inMain   bool
	aborted  atomic.Bool
	executed atomic.Bool

	mu       sync.Mutex
	paused   bool
	resumed  chan struct{}
	seq      int
	sessions []string
	seeds    map[string]seed
	warned   map[string]bool
	release  func()
}

type seed struct {
	prompt string
	skills []string
}

// NewRun prepares a run of script. Nothing executes until Execute.
func NewRun(script *Script, backend Backend, rec Recorder, opts Options) *Run {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		opts.Rand = rand.Float64
	}
	return &Run{
		script:   script,
		backend:  backend,
		rec:      rec,
		opts:     opts,
		sessions: []string{opts.InvokingSessionID},
		seeds:    map[string]seed{},
		warned:   map[string]bool{},
	}
}

// Execute runs the script's top-level code and then main(input), returning
// main's result as JSON. It returns ErrAborted if the run was aborted, and
// an error without running anything if the run already executed.
func (r *Run) Execute(ctx context.Context, input Input) (json.RawMessage, error) {
	if r.executed.Swap(true) {
		return nil, errors.New("workflow run already executed")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	vm := goja.New()
	r.mu.Lock()
	r.ctx, r.cancel, r.vm = ctx, cancel, vm
	r.mu.Unlock()
	if r.aborted.Load() {
		return nil, ErrAborted
	}
	defer r.releaseHold()
	// A script stuck in pure JS never reaches a primitive to notice its
	// context ended, so cancellation interrupts the VM directly.
	stop := context.AfterFunc(ctx, func() { vm.Interrupt(ErrAborted) })
	defer stop()

	if err := vm.Set("__native", r.native); err != nil {
		return nil, err
	}
	if _, err := vm.RunScript("prelude.js", prelude); err != nil {
		return nil, fmt.Errorf("workflow prelude: %w", err)
	}
	if _, err := vm.RunScript(r.script.Path, r.script.Source); err != nil {
		return nil, r.scriptError(err)
	}
	main, ok := goja.AssertFunction(vm.Get("main"))
	if !ok {
		return nil, errors.New("workflow has no main function")
	}
	r.inMain = true
	result, err := main(goja.Undefined(), vm.ToValue(map[string]any{"raw": input.Raw}))
	if err != nil {
		return nil, r.scriptError(err)
	}
	if goja.IsUndefined(result) || goja.IsNull(result) {
		return nil, nil
	}
	out, err := json.Marshal(result.Export())
	if err != nil {
		return nil, fmt.Errorf("encode main result: %w", err)
	}
	return out, nil
}

func (r *Run) scriptError(err error) error {
	var interrupted *goja.InterruptedError
	if r.aborted.Load() || errors.As(err, &interrupted) {
		return ErrAborted
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		// Point at the script's own line, not the prelude frame that threw.
		where := r.script.Path
		for _, frame := range ex.Stack() {
			if frame.SrcName() == r.script.Path {
				where = frame.Position().String()
				break
			}
		}
		return fmt.Errorf("%s: %s", where, ex.Value().String())
	}
	return fmt.Errorf("workflow %s: %w", r.script.Path, err)
}

// Pause stops new primitives from being issued. A primitive already in
// flight finishes.
func (r *Run) Pause() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.paused {
		r.paused = true
		r.resumed = make(chan struct{})
	}
}

// Resume continues a paused run.
func (r *Run) Resume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		r.paused = false
		close(r.resumed)
	}
}

// Abort cancels in-flight work and interrupts the script.
func (r *Run) Abort() {
	r.aborted.Store(true)
	r.mu.Lock()
	cancel, vm := r.cancel, r.vm
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if vm != nil {
		vm.Interrupt(ErrAborted)
	}
}

// Sessions lists the sessions the run has touched, invoking session first.
func (r *Run) Sessions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sessions)
}

func (r *Run) waitUnpaused() error {
	for {
		r.mu.Lock()
		paused, resumed := r.paused, r.resumed
		r.mu.Unlock()
		if !paused {
			return r.ctx.Err()
		}
		select {
		case <-resumed:
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	}
}

func (r *Run) nextSeq() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return r.seq
}

// native is the single entry point from JS. It takes a primitive name and
// JSON arguments and returns a JSON envelope, so what the script receives
// is exactly what the log records.
func (r *Run) native(name, args string) string {
	if !r.inMain {
		return encodeEnvelope(nil, errors.New("primitives can't be called at top level; call them from main"), false)
	}
	prim, ok := primitives[name]
	if !ok {
		return encodeEnvelope(nil, fmt.Errorf("unknown primitive %q", name), false)
	}
	// Releasing a hold never waits behind pause: a run paused inside
	// workflow.exclusive would otherwise keep its sessions held.
	if name != "workflow.release" {
		if err := r.waitUnpaused(); err != nil {
			return encodeEnvelope(nil, err, r.aborted.Load())
		}
	}
	value, err := r.call(r.ctx, 0, 0, name, json.RawMessage(args), func(seq int) (any, error) {
		return prim(r, seq, json.RawMessage(args))
	})
	return encodeEnvelope(value, err, r.aborted.Load())
}

// call records a started event, runs fn, and records its outcome.
func (r *Run) call(ctx context.Context, parent, index int, name string, args json.RawMessage, fn func(seq int) (any, error)) (any, error) {
	seq := r.nextSeq()
	sum := sha256.Sum256(args)
	if err := r.rec.Record(ctx, Event{
		Seq: seq, Parent: parent, Index: index, Primitive: name, Phase: PhaseStarted,
		Args: args, ArgsHash: hex.EncodeToString(sum[:]), At: r.opts.Now(),
	}); err != nil {
		return nil, fmt.Errorf("record %s: %w", name, err)
	}
	value, err := fn(seq)
	done := Event{Seq: seq, Parent: parent, Index: index, Primitive: name, At: r.opts.Now()}
	if err == nil {
		if done.Result, err = json.Marshal(value); err != nil {
			value, done.Result, err = nil, nil, fmt.Errorf("encode %s result: %w", name, err)
		}
	}
	if err != nil {
		done.Phase = PhaseFailed
		done.Error, _ = json.Marshal(describeError(err, r.aborted.Load()))
	} else {
		done.Phase = PhaseCompleted
	}
	// Recording the outcome uses a context that survives abort, so an
	// aborted call still leaves a complete log.
	if recErr := r.rec.Record(context.WithoutCancel(ctx), done); recErr != nil {
		return nil, fmt.Errorf("record %s: %w", name, recErr)
	}
	return value, err
}

func (r *Run) releaseHold() {
	r.mu.Lock()
	release := r.release
	r.release = nil
	r.mu.Unlock()
	if release != nil {
		release()
	}
}

// errorInfo is how a Go error reaches the script: name selects the JS
// error class, fields are copied onto the error object.
type errorInfo struct {
	Name    string         `json:"name"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func describeError(err error, aborted bool) errorInfo {
	var bashErr *BashError
	var schemaErr *SchemaError
	var jobsErr *JobsRunningError
	if errors.As(err, &bashErr) {
		return errorInfo{Name: "BashError", Message: err.Error(), Fields: map[string]any{
			"stdout": bashErr.Stdout, "stderr": bashErr.Stderr, "exitCode": bashErr.ExitCode,
		}}
	}
	if errors.As(err, &jobsErr) {
		return errorInfo{Name: "JobsRunning", Message: err.Error(), Fields: map[string]any{"jobs": jobsErr.Jobs}}
	}
	if errors.As(err, &schemaErr) {
		return errorInfo{Name: "SchemaError", Message: err.Error(), Fields: map[string]any{"raw": schemaErr.Raw}}
	}
	if errors.Is(err, context.Canceled) && !aborted {
		return errorInfo{Name: "Cancelled", Message: "cancelled"}
	}
	return errorInfo{Name: "Error", Message: err.Error()}
}

func encodeEnvelope(value any, err error, aborted bool) string {
	var env struct {
		OK    bool       `json:"ok"`
		Value any        `json:"value,omitempty"`
		Error *errorInfo `json:"error,omitempty"`
	}
	if err != nil {
		info := describeError(err, aborted)
		env.Error = &info
	} else {
		env.OK, env.Value = true, value
	}
	// The value already encoded when call recorded it, and every errorInfo
	// field encodes, so this can't fail.
	out, _ := json.Marshal(env)
	return string(out)
}

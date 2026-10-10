package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/itchyny/gojq"
	"mvdan.cc/sh/v3/interp"
)

const jqUsage = `jq - Go implementation of jq (gojq 0.12.19 builtin)

Synopsis:
  % echo '{"foo": 128}' | jq '.foo'

Usage:
  jq [OPTIONS] [FILTER] [FILE...]

Options:
  -r, --raw-output              output raw strings
  -j, --join-output             implies -r with no newline delimiter
  -c, --compact-output          output without pretty-printing
  -s, --slurp                   read all inputs into an array
  -n, --null-input              use null as input value
  -e, --exit-status             exit 1 when the last value is false or null
  -R, --raw-input               read input as raw strings
      --arg name value          set a string value to a variable
      --argjson name value      set a JSON value to a variable
  -h, --help                    display this help
`

// jqOptions is the parsed jq command line.
type jqOptions struct {
	help       bool
	rawOutput  bool
	compact    bool
	slurp      bool
	nullInput  bool
	exitStatus bool
	rawInput   bool
	joinOutput bool
	query      string
	files      []string
	varNames   []string
	varValues  []any
}

// jqSwitches maps each value-less jq flag to the option it sets.
var jqSwitches = map[string]func(*jqOptions){
	"-h":               func(o *jqOptions) { o.help = true },
	"--help":           func(o *jqOptions) { o.help = true },
	"-r":               func(o *jqOptions) { o.rawOutput = true },
	"--raw-output":     func(o *jqOptions) { o.rawOutput = true },
	"-j":               func(o *jqOptions) { o.joinOutput, o.rawOutput = true, true },
	"--join-output":    func(o *jqOptions) { o.joinOutput, o.rawOutput = true, true },
	"-c":               func(o *jqOptions) { o.compact = true },
	"--compact-output": func(o *jqOptions) { o.compact = true },
	"-s":               func(o *jqOptions) { o.slurp = true },
	"--slurp":          func(o *jqOptions) { o.slurp = true },
	"-n":               func(o *jqOptions) { o.nullInput = true },
	"--null-input":     func(o *jqOptions) { o.nullInput = true },
	"-e":               func(o *jqOptions) { o.exitStatus = true },
	"--exit-status":    func(o *jqOptions) { o.exitStatus = true },
	"-R":               func(o *jqOptions) { o.rawInput = true },
	"--raw-input":      func(o *jqOptions) { o.rawInput = true },
}

// jqVariableFlags maps each jq flag that binds a variable to the parser for
// its value.
var jqVariableFlags = map[string]func(name, value string) (any, error){
	"--arg": func(_, value string) (any, error) { return value, nil },
	"--argjson": func(name, value string) (any, error) {
		var val any
		if err := json.Unmarshal([]byte(value), &val); err != nil {
			return nil, fmt.Errorf("invalid JSON for --argjson %s: %w", name, err)
		}
		return val, nil
	},
}

// parseJQArgs parses jq's arguments, excluding the command name. The first
// non-flag argument is the filter and later ones are files. Parsing stops at
// the first help flag, so later arguments are never checked.
func parseJQArgs(args []string) (jqOptions, error) {
	var opts jqOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if set, ok := jqSwitches[arg]; ok {
			set(&opts)
			if opts.help {
				break
			}
			continue
		}
		if parse, ok := jqVariableFlags[arg]; ok {
			if i+2 >= len(args) {
				return opts, fmt.Errorf("%s requires name and value", arg)
			}
			val, err := parse(args[i+1], args[i+2])
			if err != nil {
				return opts, err
			}
			opts.varNames = append(opts.varNames, "$"+args[i+1])
			opts.varValues = append(opts.varValues, val)
			i += 2
			continue
		}
		if arg == "--" {
			opts.files = append(opts.files, args[i+1:]...)
			break
		}
		if opts.query == "" {
			opts.query = arg
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return opts, fmt.Errorf("unknown option: %s", arg)
		}
		opts.files = append(opts.files, arg)
	}
	if opts.query == "" {
		opts.query = "."
	}
	return opts, nil
}

// handleJQ implements the jq builtin using gojq. It supports the subset of
// jq flags listed in [jqSwitches] and [jqVariableFlags].
//
// ctx is polled at each iteration of the output loop and at each reader in
// [readInputs] so that hook timeouts or other cancellations can interrupt
// long-running queries. A cancelled context surfaces as ctx.Err(), not an
// [interp.ExitStatus], so callers (e.g. the hook runner) can distinguish
// "filter exited non-zero" from "we ran out of time".
//
// Note that this is somewhat of a reimplmentation of the CLI of the glorious
// github.com/itchyny/gojq, and we'd ideally get the CLI exposed upstream to
// avoid this falling out of sync.
func handleJQ(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Fast-fail when ctx is already cancelled so callers don't pay for
	// flag parsing and gojq compilation on a doomed request.
	if err := ctx.Err(); err != nil {
		return err
	}

	opts, err := parseJQArgs(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "jq: %s\n", err)
		return interp.ExitStatus(2)
	}
	if opts.help {
		fmt.Fprint(stdout, jqUsage)
		return nil
	}

	query, err := gojq.Parse(opts.query)
	if err != nil {
		fmt.Fprintf(stderr, "jq: %s\n", err)
		return interp.ExitStatus(3)
	}

	code, err := gojq.Compile(query,
		gojq.WithEnvironLoader(os.Environ),
		gojq.WithVariables(opts.varNames),
	)
	if err != nil {
		fmt.Fprintf(stderr, "jq: %s\n", err)
		return interp.ExitStatus(3)
	}

	// Build input values.
	inputs, err := readInputs(ctx, stdin, opts)
	if err != nil {
		// Prefer surfacing ctx cancellation verbatim so timeouts are
		// distinguishable from user input errors.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		fmt.Fprintf(stderr, "jq: %s\n", err)
		return interp.ExitStatus(2)
	}

	var lastFalsy bool
	for _, input := range inputs {
		iter := code.Run(input, opts.varValues...)
		for {
			// Poll ctx on every value so a long-running filter (e.g. a
			// generator over a slurped array) can be interrupted by hook
			// timeouts without waiting for iter.Next to yield.
			if err := ctx.Err(); err != nil {
				return err
			}
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := v.(error); ok {
				fmt.Fprintf(stderr, "jq: %s\n", err)
				return interp.ExitStatus(5)
			}
			lastFalsy = v == nil || v == false
			if err := writeValue(stdout, v, opts.rawOutput, opts.compact, opts.joinOutput); err != nil {
				return err
			}
		}
	}

	if opts.exitStatus && lastFalsy {
		return interp.ExitStatus(1)
	}
	return nil
}

// readInputs reads JSON (or raw) input values from stdin or files.
//
// ctx is polled in three places so that a cancellation observed mid-read
// short-circuits promptly:
//   - between readers (before opening the next file / consuming stdin);
//   - on every io.Read call via ctxReader, so io.ReadAll on a large but
//     non-blocking source (e.g. the bytes.NewReader payload the hook
//     runner supplies) returns ctx.Err() on the next chunk boundary;
//   - inside the post-read value accumulation loops (raw-input line
//     split and JSON stream decode), which are otherwise unbounded in
//     the size of the input.
//
// A reader that blocks forever in Read (e.g. an unterminated pipe) can
// still outlast ctx; the outer abandon-goroutine path in the hook
// runner (internal/hooks/runner.go) is the authoritative enforcer for
// that case.
func readInputs(ctx context.Context, stdin io.Reader, opts jqOptions) ([]any, error) {
	if opts.nullInput {
		return []any{nil}, nil
	}

	var readers []io.Reader
	if len(opts.files) > 0 {
		for _, f := range opts.files {
			file, err := os.Open(f)
			if err != nil {
				return nil, err
			}
			defer file.Close()
			readers = append(readers, file)
		}
	} else {
		readers = []io.Reader{stdin}
	}

	decode := decodeJSONInput
	if opts.rawInput {
		decode = decodeRawInput
	}

	var vals []any
	for _, r := range readers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := io.ReadAll(ctxReader{ctx: ctx, r: r})
		if err != nil {
			// ctxReader surfaces ctx.Err() verbatim; preserve it so the
			// caller can distinguish cancellation from a parse error.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		decoded, err := decode(ctx, data, opts.slurp)
		if err != nil {
			return nil, err
		}
		vals = append(vals, decoded...)
	}

	if len(vals) == 0 {
		return []any{nil}, nil
	}
	return vals, nil
}

// decodeRawInput turns one input into jq values for --raw-input: one string
// per line, or the whole text as a single string when slurping.
func decodeRawInput(ctx context.Context, data []byte, slurp bool) ([]any, error) {
	if slurp {
		return []any{string(data)}, nil
	}
	var vals []any
	for line := range strings.SplitSeq(string(data), "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vals = append(vals, line)
	}
	return vals, nil
}

// decodeJSONInput turns one input into jq values: each JSON value in the
// stream, or the whole stream as a single array when slurping.
func decodeJSONInput(ctx context.Context, data []byte, slurp bool) ([]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	var vals []any
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("parse error: %w", err)
		}
		vals = append(vals, v)
	}
	if slurp {
		return []any{vals}, nil
	}
	return vals, nil
}

// ctxReader wraps an io.Reader so that each Read call checks ctx first.
// This makes io.ReadAll over a large but non-blocking source (e.g. a
// bytes.Reader of the hook stdin payload) cancellable on the next chunk
// boundary. A reader that itself blocks in Read will still outlast ctx —
// the hook runner's abandon-goroutine path is the enforcer of last resort
// for that case.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

// writeValue writes a single jq output value.
func writeValue(w io.Writer, v any, raw, compact, join bool) error {
	if raw {
		if s, ok := v.(string); ok {
			if _, err := fmt.Fprint(w, s); err != nil {
				return err
			}
			if !join {
				_, err := fmt.Fprint(w, "\n")
				return err
			}
			return nil
		}
	}

	var bs []byte
	var err error
	if compact {
		bs, err = gojq.Marshal(v)
	} else {
		bs, err = json.MarshalIndent(v, "", "  ")
	}
	if err != nil {
		return err
	}
	if _, writeErr := w.Write(bs); writeErr != nil {
		return writeErr
	}
	_, err = fmt.Fprint(w, "\n")
	return err
}

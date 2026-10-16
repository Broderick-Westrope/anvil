package shell

import (
	"context"
	"fmt"
	"io"

	"github.com/Broderick-Westrope/anvil/internal/wtp"
	"mvdan.cc/sh/v3/interp"
)

// handleWTP runs the embedded wtp worktree manager in the shell's current
// directory and environment.
func handleWTP(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	err := wtp.Run(ctx, args, wtp.Env{
		Dir:     hc.Dir,
		Stdin:   readerOrEmpty(hc.Stdin),
		Stdout:  hc.Stdout,
		Stderr:  hc.Stderr,
		Environ: execEnvList(hc.Env),
	})
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	fmt.Fprintln(hc.Stderr, err)
	return interp.ExitStatus(1)
}

func readerOrEmpty(r io.Reader) io.Reader {
	if r == nil {
		return eofReader{}
	}
	return r
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

type externalWTPKey struct{}

// WithExternalWTP makes shells run `wtp` as a child process (prefix plus
// its arguments, e.g. the anvil binary and "wtp") instead of in-process.
// The embedded wtp runs repository hooks without a context, so cancelling
// an in-process `wtp add` can't stop a hung hook; as a child process it
// shares the command's process group, which cancellation kills whole.
func WithExternalWTP(ctx context.Context, prefix []string) context.Context {
	return context.WithValue(ctx, externalWTPKey{}, prefix)
}

func externalWTP(ctx context.Context) []string {
	prefix, _ := ctx.Value(externalWTPKey{}).([]string)
	return prefix
}

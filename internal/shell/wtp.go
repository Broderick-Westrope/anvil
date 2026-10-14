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

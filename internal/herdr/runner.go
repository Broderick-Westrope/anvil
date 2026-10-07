package herdr

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const commandTimeout = 5 * time.Second

type runner interface {
	run(ctx context.Context, args ...string) ([]byte, error)
}

// cliRunner is the only place that execs herdr.
type cliRunner struct {
	bin string
	env []string
}

// newCLIRunner freezes the child environment: the startup environment
// with HERDR_PANE_ID and HERDR_SOCKET_PATH overridden to cfg's values.
func newCLIRunner(cfg Config, startupEnv []string) cliRunner {
	env := make([]string, 0, len(startupEnv)+2)
	for _, kv := range startupEnv {
		if strings.HasPrefix(kv, "HERDR_PANE_ID=") || strings.HasPrefix(kv, "HERDR_SOCKET_PATH=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HERDR_PANE_ID="+cfg.PaneID, "HERDR_SOCKET_PATH="+cfg.SocketPath)
	return cliRunner{bin: cfg.Bin, env: env}
}

func (r cliRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env = r.env
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		name := strings.Join(args[:min(2, len(args))], " ")
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("herdr %s: %w: %s", name, err, msg)
		}
		return out, fmt.Errorf("herdr %s: %w", name, err)
	}
	return out, nil
}

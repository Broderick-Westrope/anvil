package reload

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
)

const (
	preflightTimeout   = 20 * time.Second
	maxPreflightStderr = 500
)

// ErrGoRun reports that the running binary was built by `go run`, so there
// is no stable binary on disk to reload.
var ErrGoRun = errors.New("anvil is running under `go run`; build or install it to reload")

// Options describe how the replacement process should start.
type Options struct {
	SessionID string
	WorkDir   string
	DataDir   string
	Debug     bool
	Yolo      config.YoloLevel
}

// Args returns the command-line arguments, excluding the program name, that
// resume o in a fresh process.
func Args(o Options) []string {
	var args []string
	if o.SessionID != "" {
		args = append(args, "--session", o.SessionID, "--there")
	} else {
		args = append(args, "--cwd", o.WorkDir)
	}
	if o.DataDir != "" {
		args = append(args, "--data-dir", o.DataDir)
	}
	if o.Debug {
		args = append(args, "--debug")
	}
	// Mirrors config.ParseYoloLevel: a bare --yolo means "true". Off is
	// passed explicitly so a yolo default in config can't override it.
	switch o.Yolo {
	case config.YoloOff:
		args = append(args, "--yolo=false")
	case config.YoloStandard:
		args = append(args, "--yolo")
	case config.YoloFull:
		args = append(args, "--yolo=full")
	}
	return args
}

// ShellQuote joins args into a string a POSIX shell parses back into the
// same arguments.
func ShellQuote(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuoteArg(a)
	}
	return strings.Join(quoted, " ")
}

func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !isShellSafe(r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isShellSafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("-_./=:,@+%", r)
}

// Executable returns the path of the anvil binary to reload, which is the
// running executable's path as it is now on disk.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Linux reports a replaced binary as "<path> (deleted)"; the new binary
	// lives at the original path.
	exe = strings.TrimSuffix(exe, " (deleted)")
	if _, err := os.Stat(exe); err != nil {
		return "", err
	}
	if isGoRunPath(exe, goBuildRoots()) {
		return "", ErrGoRun
	}
	return exe, nil
}

// goBuildRoots returns the directories under which `go run` places its
// temporary binaries.
func goBuildRoots() []string {
	var roots []string
	if d := os.Getenv("GOTMPDIR"); d != "" {
		roots = append(roots, d)
	}
	roots = append(roots, os.TempDir())
	if d, err := os.UserCacheDir(); err == nil {
		roots = append(roots, filepath.Join(d, "go-build"))
	}
	return roots
}

// isGoRunPath reports whether exe sits under one of roots in a path element
// starting with "go-build", as `go run` binaries do. Each root is compared
// both as given and with symlinks resolved.
func isGoRunPath(exe string, roots []string) bool {
	candidates := []string{filepath.Clean(exe)}
	if r, err := filepath.EvalSymlinks(exe); err == nil && r != candidates[0] {
		candidates = append(candidates, r)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		rootForms := []string{filepath.Clean(root)}
		if r, err := filepath.EvalSymlinks(root); err == nil && r != rootForms[0] {
			rootForms = append(rootForms, r)
		}
		for _, c := range candidates {
			for _, rf := range rootForms {
				if underGoBuild(c, rf) {
					return true
				}
			}
		}
	}
	return false
}

func underGoBuild(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if filepath.Base(root) == "go-build" {
		return true
	}
	for _, elem := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
		if strings.HasPrefix(elem, "go-build") {
			return true
		}
	}
	return false
}

// Preflight runs `<exe> preflight` against workDir and dataDir with the
// startup environment and returns the version the binary reports.
func Preflight(ctx context.Context, exe, workDir, dataDir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()

	args := []string{"preflight", "--cwd", workDir}
	if dataDir != "" {
		args = append(args, "--data-dir", dataDir)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = workDir
	cmd.Env = StartupEnv()
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w after %s", ctx.Err(), preflightTimeout)
		}
		// Collapse the padding fang adds around errors so the limit is
		// spent on the message.
		if msg := truncate(strings.Join(strings.Fields(stderr.String()), " "), maxPreflightStderr); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}

	line, _, _ := bufio.NewReader(&stdout).ReadLine()
	return strings.TrimSpace(string(line)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

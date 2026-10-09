package shell

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), "git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func TestRun_WtpBuiltinUsesShellDir(t *testing.T) {
	repo := initGitRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: `cd sub && wtp cd @`,
		Cwd:     repo,
		Env:     []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v (stderr: %s)", err, stderr.String())
	}
	if got := filepath.FromSlash(strings.TrimSpace(stdout.String())); got != repo {
		t.Fatalf("wtp cd @ = %q, want %q", got, repo)
	}
}

func TestRun_WtpBuiltinReportsFailureExitStatus(t *testing.T) {
	repo := initGitRepo(t)

	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: `wtp cd does-not-exist; echo "exit=$?"`,
		Cwd:     repo,
		Env:     []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "exit=1\n" {
		t.Fatalf("stdout = %q, want %q", got, "exit=1\n")
	}
	if !strings.Contains(stderr.String(), "does-not-exist") {
		t.Fatalf("stderr = %q, want it to mention the missing worktree", stderr.String())
	}
}

func TestReaderOrEmpty(t *testing.T) {
	data, err := io.ReadAll(readerOrEmpty(nil))
	if err != nil || len(data) != 0 {
		t.Fatalf("readerOrEmpty(nil) read %q, %v; want an empty stream so wtp never falls back to Anvil's own stdin", data, err)
	}

	in := strings.NewReader("piped")
	if got := readerOrEmpty(in); got != in {
		t.Fatalf("readerOrEmpty(r) = %v, want r itself", got)
	}
}

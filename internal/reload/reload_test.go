package reload

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "session",
			opts: Options{SessionID: "abc", WorkDir: "/w"},
			want: []string{"--session", "abc", "--there"},
		},
		{
			name: "no session",
			opts: Options{WorkDir: "/w"},
			want: []string{"--cwd", "/w"},
		},
		{
			name: "all flags standard yolo",
			opts: Options{SessionID: "abc", DataDir: "/d", Debug: true, Yolo: config.YoloStandard},
			want: []string{"--session", "abc", "--there", "--data-dir", "/d", "--debug", "--yolo"},
		},
		{
			name: "full yolo",
			opts: Options{WorkDir: "/w", Yolo: config.YoloFull},
			want: []string{"--cwd", "/w", "--yolo=full"},
		},
		{
			name: "yolo off",
			opts: Options{WorkDir: "/w", Yolo: config.YoloOff},
			want: []string{"--cwd", "/w"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Args(tt.opts))
		})
	}
}

// The yolo flag strings must round-trip through the root command's parser:
// a bare --yolo takes NoOptDefVal "true".
func TestArgsYoloMatchesParser(t *testing.T) {
	t.Parallel()
	for _, level := range []config.YoloLevel{config.YoloOff, config.YoloStandard, config.YoloFull} {
		args := Args(Options{WorkDir: "/w", Yolo: level})
		value := ""
		for _, a := range args {
			switch {
			case a == "--yolo":
				value = "true"
			case strings.HasPrefix(a, "--yolo="):
				value = strings.TrimPrefix(a, "--yolo=")
			}
		}
		got, err := config.ParseYoloLevel(value)
		require.NoError(t, err)
		require.Equal(t, level, got)
	}
}

func TestShellQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--session", "abc-123", "--there"}, "--session abc-123 --there"},
		{[]string{"--cwd", "/path/with space"}, "--cwd '/path/with space'"},
		{[]string{"it's"}, `'it'\''s'`},
		{[]string{""}, "''"},
		{[]string{"$(touch x)"}, "'$(touch x)'"},
		{[]string{"--yolo=full", "a@b:c,d+e%f"}, "--yolo=full a@b:c,d+e%f"},
		{[]string{"~/x", "a*b"}, "'~/x' 'a*b'"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, ShellQuote(tt.args), "args %q", tt.args)
	}
}

func TestShellQuoteRoundTrip(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX quoting")
	}
	args := []string{"plain", "with space", "it's", "", "$(touch x)", "a\nb", `back\slash`}
	out, err := exec.CommandContext(t.Context(), "sh", "-c", `eval "set -- $1"; for a in "$@"; do printf '%s\0' "$a"; done`, "sh", ShellQuote(args)).Output()
	require.NoError(t, err)
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.Equal(t, args, got)
}

func TestIsGoRunPath(t *testing.T) {
	t.Parallel()
	tmp := filepath.FromSlash("/tmp")
	cache := filepath.FromSlash("/home/u/.cache/go-build")
	roots := []string{tmp, cache}
	tests := []struct {
		path string
		want bool
	}{
		{"/tmp/go-build123456/b001/exe/anvil", true},
		{"/tmp/go-build123456/exe/anvil", true},
		{"/home/u/.cache/go-build/ab/cdef/anvil", true},
		{"/tmp/anvil-reload/anvil", false},
		{"/tmp/anvil", false},
		{"/usr/local/bin/anvil", false},
		{"/home/u/go/bin/anvil", false},
		{"/opt/go-build123/anvil", false},
		{"/tmp/go-builder-notes/../anvil", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, isGoRunPath(filepath.FromSlash(tt.path), roots), tt.path)
	}
}

func TestExecutableRefusesGoBuildBinary(t *testing.T) {
	t.Parallel()
	self, err := os.Executable()
	require.NoError(t, err)
	if !strings.Contains(self, "go-build") {
		t.Skip("test binary isn't in a go-build dir")
	}
	_, err = Executable()
	require.ErrorIs(t, err, ErrGoRun)
}

func buildFakeBin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "fakebin")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./testdata/fakebin").CombinedOutput()
	require.NoError(t, err, string(out))
	return bin
}

// Not parallel: uses t.Setenv and the startup capture.
func TestPreflight(t *testing.T) {
	bin := buildFakeBin(t)

	t.Run("pass", func(t *testing.T) {
		resetStartup(t)
		t.Setenv("FAKEBIN_MODE", "pass")
		CaptureStartup()

		version, err := Preflight(context.Background(), bin, t.TempDir(), "")
		require.NoError(t, err)
		require.Equal(t, "v9.9.9-fake", version)
	})

	t.Run("args and env", func(t *testing.T) {
		resetStartup(t)
		t.Setenv("FAKEBIN_MODE", "echo")
		t.Setenv(EnvHandoff, "/should/not/leak")
		CaptureStartup()

		wd, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		got, err := Preflight(context.Background(), bin, wd, "/data")
		require.NoError(t, err)
		require.Equal(t, "args=preflight --cwd "+wd+" --data-dir /data wd="+wd+" handoff=unset", got)

		got, err = Preflight(context.Background(), bin, wd, "")
		require.NoError(t, err)
		require.Equal(t, "args=preflight --cwd "+wd+" wd="+wd+" handoff=unset", got)
	})

	t.Run("fail", func(t *testing.T) {
		resetStartup(t)
		t.Setenv("FAKEBIN_MODE", "fail")
		CaptureStartup()

		_, err := Preflight(context.Background(), bin, t.TempDir(), "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "Error: xxx")
		require.LessOrEqual(t, strings.Count(err.Error(), "x"), maxPreflightStderr)
	})

	t.Run("uses startup env", func(t *testing.T) {
		resetStartup(t)
		CaptureStartup()
		t.Setenv("FAKEBIN_MODE", "fail")

		_, err := Preflight(context.Background(), bin, t.TempDir(), "")
		require.NoError(t, err, "variables set after capture must not reach the child")
	})
}

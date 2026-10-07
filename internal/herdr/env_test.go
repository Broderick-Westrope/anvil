package herdr

import (
	"io/fs"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeInfo struct {
	name string
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

func fakeStat(files map[string]fs.FileMode) func(string) (fs.FileInfo, error) {
	return func(path string) (fs.FileInfo, error) {
		mode, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return fakeInfo{name: path, mode: mode}, nil
	}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket and permission semantics")
	}

	files := map[string]fs.FileMode{
		"/run/herdr.sock":      fs.ModeSocket | 0o600,
		"/run/plain":           0o600,
		"/opt/herdr/bin/herdr": 0o755,
		"/usr/bin/herdr":       0o755,
		"/noexec/herdr":        0o644,
		"herdr":                0o755,
		"rel/herdr":            0o755,
	}
	base := []string{
		"HERDR_ENV=1",
		"HERDR_PANE_ID=w1:p2",
		"HERDR_SOCKET_PATH=/run/herdr.sock",
		"PATH=/usr/bin",
	}
	with := func(extra ...string) []string {
		return append(slices.Clone(base), extra...)
	}

	tests := []struct {
		name   string
		env    []string
		want   Config
		reason string
		ok     bool
	}{
		{
			name: "not in herdr",
			env:  []string{"HERDR_PANE_ID=w1:p2", "PATH=/usr/bin"},
		},
		{
			name:   "missing pane",
			env:    []string{"HERDR_ENV=1", "HERDR_SOCKET_PATH=/run/herdr.sock"},
			reason: "HERDR_PANE_ID not set",
		},
		{
			name:   "nested",
			env:    with(EnvReporting + "=1"),
			reason: "parent Anvil already reports to this pane",
		},
		{
			name:   "socket path missing",
			env:    with("HERDR_SOCKET_PATH=/run/missing.sock"),
			reason: "HERDR_SOCKET_PATH is not a socket",
		},
		{
			name:   "socket path empty",
			env:    with("HERDR_SOCKET_PATH="),
			reason: "HERDR_SOCKET_PATH is not a socket",
		},
		{
			name:   "socket path not a socket",
			env:    with("HERDR_SOCKET_PATH=/run/plain"),
			reason: "HERDR_SOCKET_PATH is not a socket",
		},
		{
			name: "bin path absolute executable",
			env:  with("HERDR_BIN_PATH=/opt/herdr/bin/herdr"),
			want: Config{Bin: "/opt/herdr/bin/herdr", PaneID: "w1:p2", SocketPath: "/run/herdr.sock"},
			ok:   true,
		},
		{
			name: "bin path relative falls back to PATH",
			env:  with("HERDR_BIN_PATH=herdr"),
			want: Config{Bin: "/usr/bin/herdr", PaneID: "w1:p2", SocketPath: "/run/herdr.sock"},
			ok:   true,
		},
		{
			name: "bin path not executable falls back to PATH",
			env:  with("HERDR_BIN_PATH=/noexec/herdr"),
			want: Config{Bin: "/usr/bin/herdr", PaneID: "w1:p2", SocketPath: "/run/herdr.sock"},
			ok:   true,
		},
		{
			name:   "relative PATH entry skipped",
			env:    with("PATH=.:rel"),
			reason: "no absolute herdr binary found",
		},
		{
			name:   "PATH entry with non-executable herdr rejected",
			env:    with("PATH=/noexec"),
			reason: "no absolute herdr binary found",
		},
		{
			name: "PATH searched past rejected entries",
			env:  with("PATH=.:/noexec:/opt/herdr/bin"),
			want: Config{Bin: "/opt/herdr/bin/herdr", PaneID: "w1:p2", SocketPath: "/run/herdr.sock"},
			ok:   true,
		},
		{
			name:   "duplicate key last wins",
			env:    with("HERDR_ENV=0"),
			reason: "",
		},
		{
			name: "duplicate pane last wins",
			env:  with("HERDR_PANE_ID=w1:p9"),
			want: Config{Bin: "/usr/bin/herdr", PaneID: "w1:p9", SocketPath: "/run/herdr.sock"},
			ok:   true,
		},
		{
			name: "full success",
			env:  base,
			want: Config{Bin: "/usr/bin/herdr", PaneID: "w1:p2", SocketPath: "/run/herdr.sock"},
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, reason, ok := Detect(tt.env, fakeStat(files))
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.reason, reason)
			require.Equal(t, tt.want, cfg)
		})
	}
}

func TestCLIRunnerEnv(t *testing.T) {
	t.Parallel()

	cfg := Config{Bin: "/usr/bin/herdr", PaneID: "w1:p2", SocketPath: "/run/herdr.sock"}
	r := newCLIRunner(cfg, []string{"HERDR_PANE_ID=old", "PATH=/x", "HERDR_SOCKET_PATH=/old.sock"})

	require.Equal(t, "/usr/bin/herdr", r.bin)
	count := 0
	for _, kv := range r.env {
		if kv == "HERDR_PANE_ID=w1:p2" {
			count++
		}
		require.NotEqual(t, "HERDR_PANE_ID=old", kv)
		require.NotEqual(t, "HERDR_SOCKET_PATH=/old.sock", kv)
	}
	require.Equal(t, 1, count)
	require.Contains(t, r.env, "HERDR_SOCKET_PATH=/run/herdr.sock")
	require.Contains(t, r.env, "PATH=/x")
}

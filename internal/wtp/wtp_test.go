package wtp

import (
	"bytes"
	"errors"
	"os"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunDefaultsVersionToEmbeddedModule(t *testing.T) {
	var stdout bytes.Buffer
	err := Run(t.Context(), []string{"wtp", "--version"}, Env{Dir: t.TempDir(), Stdout: &stdout, Stderr: &stdout})
	require.NoError(t, err)
	require.Equal(t, "wtp version "+version()+"\n", stdout.String())
	require.NotEmpty(t, version(), "the test binary embeds the wtp module")
}

func TestRunKeepsExplicitVersion(t *testing.T) {
	var stdout bytes.Buffer
	err := Run(t.Context(), []string{"wtp", "--version"}, Env{Dir: t.TempDir(), Stdout: &stdout, Stderr: &stdout, Version: "v9.9.9"})
	require.NoError(t, err)
	require.Equal(t, "wtp version v9.9.9\n", stdout.String())
}

func TestRunDefaultsSelfToAnvilWtp(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)

	var stdout bytes.Buffer
	err = Run(t.Context(), []string{"wtp", "hook", "bash"}, Env{Dir: t.TempDir(), Stdout: &stdout, Stderr: &stdout})
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "command '"+exe+"' 'wtp' ")
}

func TestRunKeepsExplicitSelf(t *testing.T) {
	var stdout bytes.Buffer
	err := Run(t.Context(), []string{"wtp", "hook", "bash"}, Env{
		Dir:    t.TempDir(),
		Stdout: &stdout,
		Stderr: &stdout,
		Self:   []string{"/opt/host", "embedded-wtp"},
	})
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "command '/opt/host' 'embedded-wtp' ")
	require.False(t, strings.Contains(stdout.String(), "'wtp' "), "the default self must not replace an explicit one")
}

func TestSelfFrom(t *testing.T) {
	require.Equal(t, []string{"/usr/local/bin/anvil", "wtp"}, selfFrom("/usr/local/bin/anvil", nil))
	require.Equal(t, []string{"anvil", "wtp"}, selfFrom("", errors.New("no executable")))
}

func TestModuleVersion(t *testing.T) {
	other := &debug.Module{Path: "example.com/other", Version: "v1.0.0"}

	tests := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{name: "no build info", ok: false, want: ""},
		{name: "module absent", info: &debug.BuildInfo{Deps: []*debug.Module{other}}, ok: true, want: ""},
		{
			name: "module after others",
			info: &debug.BuildInfo{Deps: []*debug.Module{other, {Path: modulePath, Version: "v3.3.0"}}},
			ok:   true,
			want: "v3.3.0",
		},
		{
			name: "versioned replacement",
			info: &debug.BuildInfo{Deps: []*debug.Module{{Path: modulePath, Version: "v3.3.0", Replace: &debug.Module{Path: "example.com/fork", Version: "v3.3.1-fork"}}}},
			ok:   true,
			want: "v3.3.1-fork",
		},
		{
			name: "local directory replacement",
			info: &debug.BuildInfo{Deps: []*debug.Module{{Path: modulePath, Version: "v3.3.0", Replace: &debug.Module{Path: "../wtp"}}}},
			ok:   true,
			want: "v3.3.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, moduleVersion(tt.info, tt.ok))
		})
	}
}

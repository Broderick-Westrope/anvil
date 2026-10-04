package reload

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHandoffRoundTrip(t *testing.T) {
	t.Parallel()
	dir := Dir(t.TempDir())
	want := Handoff{
		SessionID:   "sess-1",
		Draft:       "half-written prompt\nwith a second line",
		YoloLevel:   "standard",
		BouncerMode: "enforce",
		FromVersion: "v1.2.3",
	}

	path, err := Write(dir, want)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		dirInfo, err := os.Stat(dir)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	}

	got, err := Load(dir, path)
	require.NoError(t, err)
	require.False(t, got.CreatedAt.IsZero())
	want.CreatedAt = got.CreatedAt
	require.Equal(t, want, got)

	_, err = os.Stat(path)
	require.NoError(t, err, "Load must not delete the handoff")

	require.NoError(t, Remove(dir, path))
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))
}

func TestLoadIgnoresAge(t *testing.T) {
	t.Parallel()
	dir := Dir(t.TempDir())
	path, err := Write(dir, Handoff{SessionID: "s", CreatedAt: time.Now().Add(-30 * 24 * time.Hour)})
	require.NoError(t, err)
	old := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))

	got, err := Load(dir, path)
	require.NoError(t, err)
	require.Equal(t, "s", got.SessionID)
}

func resolvedDir(t *testing.T) string {
	t.Helper()
	dir := Dir(t.TempDir())
	require.NoError(t, os.MkdirAll(dir, 0o700))
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

func TestLoadRejects(t *testing.T) {
	t.Parallel()

	t.Run("outside dir", func(t *testing.T) {
		t.Parallel()
		dir := resolvedDir(t)
		other, err := Write(Dir(t.TempDir()), Handoff{SessionID: "s"})
		require.NoError(t, err)
		_, err = Load(dir, other)
		require.ErrorContains(t, err, "outside")
	})

	t.Run("traversal", func(t *testing.T) {
		t.Parallel()
		dir := resolvedDir(t)
		outside := filepath.Join(filepath.Dir(dir), "handoff.json")
		require.NoError(t, os.WriteFile(outside, []byte(`{}`), 0o600))
		_, err := Load(dir, filepath.Join(dir, "..", "handoff.json"))
		require.ErrorContains(t, err, "outside")
	})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on Windows")
		}
		dir := resolvedDir(t)
		target := filepath.Join(t.TempDir(), "target.json")
		require.NoError(t, os.WriteFile(target, []byte(`{"session_id":"s"}`), 0o600))
		link := filepath.Join(dir, "link.json")
		require.NoError(t, os.Symlink(target, link))
		_, err := Load(dir, link)
		require.ErrorContains(t, err, "not a regular file")
	})

	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		dir := resolvedDir(t)
		sub := filepath.Join(dir, "sub")
		require.NoError(t, os.Mkdir(sub, 0o700))
		_, err := Load(dir, sub)
		require.ErrorContains(t, err, "not a regular file")
	})

	t.Run("too large", func(t *testing.T) {
		t.Parallel()
		dir := resolvedDir(t)
		path := filepath.Join(dir, "big.json")
		big := `{"draft":"` + strings.Repeat("x", maxHandoffSize) + `"}`
		require.NoError(t, os.WriteFile(path, []byte(big), 0o600))
		_, err := Load(dir, path)
		require.ErrorContains(t, err, "limit")
	})

	t.Run("bad json", func(t *testing.T) {
		t.Parallel()
		dir := resolvedDir(t)
		path := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(path, []byte(`{not json`), 0o600))
		_, err := Load(dir, path)
		require.ErrorContains(t, err, "decode")
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		dir := resolvedDir(t)
		_, err := Load(dir, filepath.Join(dir, "missing.json"))
		require.True(t, os.IsNotExist(err))
	})
}

func TestRemoveRefusesOutsideDir(t *testing.T) {
	t.Parallel()
	dir := resolvedDir(t)
	victim := filepath.Join(t.TempDir(), "victim.json")
	require.NoError(t, os.WriteFile(victim, []byte(`{}`), 0o600))

	require.Error(t, Remove(dir, victim))
	_, err := os.Stat(victim)
	require.NoError(t, err)
}

func TestSweep(t *testing.T) {
	t.Parallel()
	dir := Dir(t.TempDir())
	now := time.Now()

	fresh, err := Write(dir, Handoff{SessionID: "fresh"})
	require.NoError(t, err)
	recent, err := Write(dir, Handoff{SessionID: "recent"})
	require.NoError(t, err)
	stale, err := Write(dir, Handoff{SessionID: "stale"})
	require.NoError(t, err)

	recentTime := now.Add(-handoffRetention + time.Hour)
	staleTime := now.Add(-handoffRetention - time.Hour)
	require.NoError(t, os.Chtimes(recent, recentTime, recentTime))
	require.NoError(t, os.Chtimes(stale, staleTime, staleTime))

	Sweep(dir, now)

	for _, p := range []string{fresh, recent} {
		_, err := os.Stat(p)
		require.NoError(t, err)
	}
	_, err = os.Stat(stale)
	require.True(t, os.IsNotExist(err))

	Sweep(filepath.Join(t.TempDir(), "missing"), now)
}

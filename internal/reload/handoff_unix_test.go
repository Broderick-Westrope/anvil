//go:build !windows

package reload

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Not parallel: swaps the package-level getuid seam.
func TestLoadRejectsWrongOwner(t *testing.T) {
	dir := Dir(t.TempDir())
	path, err := Write(dir, Handoff{SessionID: "s"})
	require.NoError(t, err)

	orig := getuid
	getuid = func() int { return os.Getuid() + 1 }
	t.Cleanup(func() { getuid = orig })

	_, err = Load(dir, path)
	require.ErrorContains(t, err, "owned by uid")
	require.Error(t, Remove(dir, path))
	_, err = os.Stat(path)
	require.NoError(t, err)
}

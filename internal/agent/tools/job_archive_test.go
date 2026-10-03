package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadArchivedIncremental_DropsStaleCursors(t *testing.T) {
	t.Parallel()

	archive, _, _ := newTestArchive(t)
	stale := archiveCursorKey{archive: archive, id: "stale"}
	fresh := archiveCursorKey{archive: archive, id: "fresh"}
	archiveCursors.mu.Lock()
	archiveCursors.m[stale] = archiveCursor{stdout: 3, touched: time.Now().Add(-2 * archiveCursorTTL)}
	archiveCursors.m[fresh] = archiveCursor{stdout: 3, touched: time.Now()}
	archiveCursors.mu.Unlock()

	res := readArchivedIncremental(archive, "read", []byte("abc"), nil, false)
	require.Equal(t, "abc", res.Stdout)

	archiveCursors.mu.Lock()
	_, staleKept := archiveCursors.m[stale]
	_, freshKept := archiveCursors.m[fresh]
	archiveCursors.mu.Unlock()
	require.False(t, staleKept, "a cursor unused for longer than the TTL is dropped")
	require.True(t, freshKept)

	again := readArchivedIncremental(archive, "read", []byte("abcdef"), nil, false)
	require.True(t, again.HadPrevious)
	require.Equal(t, "def", again.Stdout)
}

package anim

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFrameInterval(t *testing.T) {
	t.Parallel()
	require.Equal(t, 50*time.Millisecond, FrameInterval())
}

// TestAdvanceMovesOneFrame pins that each Advance is exactly one frame: the
// UI's shared clock calls it once per tick, so anything more would speed
// spinners up.
func TestAdvanceMovesOneFrame(t *testing.T) {
	t.Parallel()

	a := New(Settings{ID: "test", Size: 5})
	for i := range 3 {
		require.Equal(t, int64(i), a.framesSinceStart.Load())
		a.Advance()
	}
	require.Equal(t, int64(3), a.framesSinceStart.Load())
}

func TestAdvanceChangesRender(t *testing.T) {
	t.Parallel()

	a := New(Settings{ID: "test", Size: 5, Label: "Working"})
	seen := map[string]struct{}{a.Render(): {}}
	for range maxBirthSteps + 2*ellipsisAnimSpeed {
		a.Advance()
		seen[a.Render()] = struct{}{}
	}
	require.Greater(t, len(seen), 3, "advancing must change what is drawn")
	require.True(t, a.initialized.Load(), "birth finishes after maxBirthSteps frames")
}

func TestAnimsAdvanceIndependently(t *testing.T) {
	t.Parallel()

	a1 := New(Settings{ID: "a1", Size: 5})
	a2 := New(Settings{ID: "a2", Size: 5})

	a1.Advance()
	require.Equal(t, int64(1), a1.framesSinceStart.Load())
	require.Equal(t, int64(0), a2.framesSinceStart.Load())
}

// TestSameIDRendersIdentically pins the deterministic birth schedule: the
// same tool call shown in the root chat and a drill-in chat looks the same
// frame for frame.
func TestSameIDRendersIdentically(t *testing.T) {
	t.Parallel()

	root := New(Settings{ID: "tool-1", Size: 5})
	drill := New(Settings{ID: "tool-1", Size: 5})
	for range 5 {
		require.Equal(t, root.Render(), drill.Render())
		root.Advance()
		drill.Advance()
	}
}

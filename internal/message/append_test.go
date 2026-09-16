package message

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAppendContent_Amortized verifies the accumulation-buffer append
// path: correctness of the final text, immutability of previously
// published snapshots, and independence of clones.
func TestAppendContent_Amortized(t *testing.T) {
	t.Parallel()

	t.Run("accumulates deltas correctly", func(t *testing.T) {
		t.Parallel()
		var m Message
		var want strings.Builder
		for i := range 500 {
			delta := fmt.Sprintf("delta-%d ", i)
			m.AppendContent(delta)
			want.WriteString(delta)
		}
		require.Equal(t, want.String(), m.Content().Text)
	})

	t.Run("earlier snapshots stay immutable across later appends", func(t *testing.T) {
		t.Parallel()
		var m Message
		m.AppendContent("hello")
		snap := m.Content().Text
		m.AppendContent(" world")
		m.AppendContent(", again and again and again")
		require.Equal(t, "hello", snap, "previously returned text must not mutate")
		require.Equal(t, "hello world, again and again and again", m.Content().Text)
	})

	t.Run("clone appends independently", func(t *testing.T) {
		t.Parallel()
		var m Message
		m.AppendContent("shared prefix")
		c := m.Clone()
		m.AppendContent(" original")
		c.AppendContent(" clone")
		require.Equal(t, "shared prefix original", m.Content().Text)
		require.Equal(t, "shared prefix clone", c.Content().Text)
	})

	t.Run("append resumes after external text (DB load)", func(t *testing.T) {
		t.Parallel()
		// Simulate a message materialized from the DB: text part
		// exists but no accumulator.
		m := Message{Parts: []ContentPart{TextContent{Text: "from db"}}}
		m.AppendContent(" plus delta")
		require.Equal(t, "from db plus delta", m.Content().Text)
	})
}

// TestAppendReasoningContent_Amortized mirrors the text tests for the
// thinking accumulation path.
func TestAppendReasoningContent_Amortized(t *testing.T) {
	t.Parallel()

	t.Run("accumulates and preserves fields", func(t *testing.T) {
		t.Parallel()
		var m Message
		m.AppendReasoningContent("think ")
		m.AppendReasoningContent("harder")
		rc := m.ReasoningContent()
		require.Equal(t, "think harder", rc.Thinking)
		require.NotZero(t, rc.StartedAt)
	})

	t.Run("snapshots stay immutable", func(t *testing.T) {
		t.Parallel()
		var m Message
		m.AppendReasoningContent("step one")
		snap := m.ReasoningContent().Thinking
		m.AppendReasoningContent(" step two")
		require.Equal(t, "step one", snap)
		require.Equal(t, "step one step two", m.ReasoningContent().Thinking)
	})

	t.Run("clone appends independently", func(t *testing.T) {
		t.Parallel()
		var m Message
		m.AppendReasoningContent("base")
		c := m.Clone()
		m.AppendReasoningContent("-orig")
		c.AppendReasoningContent("-clone")
		require.Equal(t, "base-orig", m.ReasoningContent().Thinking)
		require.Equal(t, "base-clone", c.ReasoningContent().Thinking)
	})
}

// BenchmarkAppendContent measures per-delta append cost at a realistic
// streamed-message size. Before the accumulation buffer each delta
// re-copied the entire accumulated string: O(n) per delta, O(n²) per
// stream.
func BenchmarkAppendContent(b *testing.B) {
	delta := strings.Repeat("x", 50)
	base := strings.Repeat("y", 512<<10) // 512KB accumulated so far.

	b.Run("steady-state-512KB", func(b *testing.B) {
		var m Message
		m.AppendContent(base)
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			m.AppendContent(delta)
		}
	})

	b.Run("full-stream-1MB", func(b *testing.B) {
		n := (1 << 20) / len(delta)
		b.ReportAllocs()
		for b.Loop() {
			var m Message
			for range n {
				m.AppendContent(delta)
			}
		}
	})
}

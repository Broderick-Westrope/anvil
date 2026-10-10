package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Relaxed boundary (upstream a2a024e7, 2586b0c1). Prose holding no blank
// line never satisfies the blank-line predicate, so without the relaxed
// cut the trailing partial grows until the forced cut at
// maxUnsafeTrailBytes. Past relaxBoundaryAfter of unstable tail we cut at
// a plain newline instead, but only where the same hazard checks pass;
// the forced cut stays as the backstop for content no check can clear.

// longProse returns n lines of plain prose joined by single newlines,
// so the document holds no blank-line separator anywhere.
func longProse(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("Let me reconsider step %d: the constraint holds for every branch here.", i)
	}
	return strings.Join(lines, "\n")
}

func TestStreamingMarkdown_RelaxedBoundaryAdvancesOnLongProse(t *testing.T) {
	t.Parallel()

	const width = 80
	doc := longProse(400)
	require.Greater(t, len(doc), maxUnsafeTrailBytes*2,
		"sanity check: doc must outgrow the forced-cut threshold")
	require.Equal(t, -1, findSafeMarkdownBoundary(doc),
		"sanity check: no blank lines, no safe boundary")

	r := newTestRenderer(t, width)
	var sm streamingMarkdown

	var lastOut string
	for _, p := range progressivePrefixes(doc, 80) {
		if p == "" {
			continue
		}
		lastOut = sm.Render(p, width, r)
		require.NotEmpty(t, lastOut)
		require.True(t, strings.HasPrefix(p, sm.stablePrefix),
			"stable prefix must stay a literal prefix of the content")
		require.LessOrEqual(t, len(p)-len(sm.stablePrefix), relaxBoundaryAfter,
			"the relaxed cut must keep the re-rendered tail within relaxBoundaryAfter")
	}

	require.NotEmpty(t, sm.stablePrefix,
		"relaxed boundary must advance the cache once the tail outgrows the threshold")
	require.False(t, sm.forced,
		"plain prose must be cut by the validated relaxed boundary, never the seamed forced cut")
	visible := stripANSI(lastOut)
	require.Contains(t, visible, "step 0:", "head of the trace must survive")
	require.Contains(t, visible, "step 399:", "tail of the trace must survive")
}

func TestStreamingMarkdown_RelaxedBoundaryRespectsHazards(t *testing.T) {
	t.Parallel()

	const width = 80

	tests := []struct {
		name string
		doc  string
	}{
		{
			// Every line holds a pipe, so lineOpensConstruct rejects
			// each candidate and the relaxed cut never splits the table.
			name: "long table",
			doc: func() string {
				rows := make([]string, 1200)
				rows[0] = "| col a | col b | col c |"
				rows[1] = "| ----- | ----- | ----- |"
				for i := 2; i < len(rows); i++ {
					rows[i] = fmt.Sprintf("| %d | %d | %d |", i, i*2, i*3)
				}
				return strings.Join(rows, "\n")
			}(),
		},
		{
			// An open fence makes the parity odd at every candidate
			// inside it, so no relaxed cut lands in the code block.
			name: "open code fence",
			doc:  "```go\n" + longProse(400),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Greater(t, len(tc.doc), maxUnsafeTrailBytes*2)

			r := newTestRenderer(t, width)
			var sm streamingMarkdown
			for _, p := range progressivePrefixes(tc.doc, 40) {
				if p == "" {
					continue
				}
				require.Equal(t, -1, sm.relaxedBoundary(p, len(sm.stablePrefix)),
					"no relaxed cut may land inside an unsafe construct")
				require.NotEmpty(t, sm.Render(p, width, r))
			}
			// The forced cut is the only thing that may advance the
			// cache here; it keeps per-flush work bounded and its seam
			// is removed by RenderFinal.
			require.True(t, sm.forced, "only the forced cut may advance through an unsafe construct")
			require.LessOrEqual(t, len(tc.doc)-len(sm.stablePrefix), maxUnsafeTrailBytes)
		})
	}
}

func TestStreamingMarkdown_RelaxedBoundaryThreshold(t *testing.T) {
	t.Parallel()

	var sm streamingMarkdown
	line := "Plain prose line that never holds a blank line in between.\n"

	atLimit := strings.Repeat(line, relaxBoundaryAfter/len(line)+1)[:relaxBoundaryAfter]
	require.Len(t, atLimit, relaxBoundaryAfter)
	require.Equal(t, -1, sm.relaxedBoundary(atLimit, 0),
		"a tail of exactly relaxBoundaryAfter must not be cut")

	over := atLimit + "x"
	got := sm.relaxedBoundary(over, 0)
	require.Positive(t, got, "a tail one byte past relaxBoundaryAfter must be cut")
	require.Equal(t, byte('\n'), over[got-1], "the cut must land just after a newline")

	require.Equal(t, -1, sm.relaxedBoundary(over, 1),
		"the threshold applies to the tail after the stable prefix, not the whole document")
}

func TestStreamingMarkdown_RelaxedBoundaryCandidateCap(t *testing.T) {
	t.Parallel()

	var sm streamingMarkdown
	prose := longProse(60) + "\n"
	rows := func(n int) string {
		return strings.Repeat("| a | b |\n", n)
	}

	// Each table row rejects the candidate after it, so the first
	// valid cut sits after the last prose line, rows+1 candidates back.
	within := prose + rows(relaxedBoundaryCandidates-1)
	require.Equal(t, len(prose), sm.relaxedBoundary(within, 0),
		"the valid cut must be found when it is within the candidate cap")

	beyond := prose + rows(relaxedBoundaryCandidates)
	require.Equal(t, -1, sm.relaxedBoundary(beyond, 0),
		"the search must stop after relaxedBoundaryCandidates rejected candidates")
}

func TestNewlineBefore(t *testing.T) {
	t.Parallel()

	require.Equal(t, -1, newlineBefore("abc\ndef", 0))
	require.Equal(t, -1, newlineBefore("abcdef", 6))
	require.Equal(t, 4, newlineBefore("abc\ndef", 7))
	require.Equal(t, 4, newlineBefore("abc\ndef\n", 8-1))
	require.Equal(t, 8, newlineBefore("abc\ndef\n", 8))
}

// TestStreamingMarkdown_LastLinesMatchesOutput pins the incrementally
// tracked line count to the value a fresh countLines would produce, on
// every render path: full, seeded, glued, relaxed, forced and final.
// renderThinking sizes its "N lines hidden" truncation hint from
// LastLines, so any drift shows up as a wrong number on screen.
func TestStreamingMarkdown_LastLinesMatchesOutput(t *testing.T) {
	t.Parallel()

	const width = 80

	docs := map[string]string{
		"blank-line-free prose": longProse(400),
		"paragraphs":            strings.Repeat("A paragraph of reasoning that runs on for a while.\n\n", 120),
		"mixed":                 longProse(60) + "\n\n" + strings.Repeat("- a list item\n", 30) + "\n\n" + longProse(200),
		"fenced":                "```go\nfunc main() {}\n```\n\n" + longProse(200),
		"table":                 "| a | b |\n| - | - |\n| 1 | 2 |\n\n" + longProse(200),
		"long open fence":       "Intro.\n\n```go\n" + strings.Repeat("x := compute(42) // keep going\n", 600),
		"giant single line":     strings.Repeat("token ", 4000),
	}

	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newTestRenderer(t, width)
			var sm streamingMarkdown
			for i, p := range progressivePrefixes(doc, 60) {
				if p == "" {
					continue
				}
				out := sm.Render(p, width, r)
				require.Equalf(t, countLines(strings.TrimSpace(out)), sm.LastLines(),
					"step %d (len=%d): tracked line count must match the output", i, len(p))
			}
			out := sm.RenderFinal(doc, width, r)
			require.Equal(t, countLines(strings.TrimSpace(out)), sm.LastLines(),
				"RenderFinal must record its own line count")

			// A width change drops the cache and full-renders.
			out = sm.Render(doc, width-10, r)
			require.Equal(t, countLines(strings.TrimSpace(out)), sm.LastLines())
		})
	}
}

func TestGlueLines(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		prefix      string
		prefixLines int
		trail       string
	}{
		{name: "empty both", prefix: "", prefixLines: 0, trail: ""},
		{name: "empty prefix", prefix: "", prefixLines: 0, trail: "a\nb"},
		{name: "empty trail", prefix: "a\nb", prefixLines: 2, trail: ""},
		{name: "whitespace trail", prefix: "a\nb", prefixLines: 2, trail: "\n  \n"},
		{name: "both", prefix: "a\nb", prefixLines: 2, trail: "c\nd\ne"},
		{name: "untrimmed trail", prefix: "a", prefixLines: 1, trail: "\nc\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, countLines(glueRenders(tc.prefix, tc.trail)),
				glueLines(tc.prefix, tc.prefixLines, tc.trail))
		})
	}
}

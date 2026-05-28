package diffview_test

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/diffview"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
)

func TestNoTruncateVisual(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		layout func(dv *diffview.DiffView) *diffview.DiffView
		width  int
	}{
		"unified_notrunc_w60": {
			layout: func(dv *diffview.DiffView) *diffview.DiffView { return dv.Unified() },
			width:  60,
		},
		"split_notrunc_w80": {
			layout: func(dv *diffview.DiffView) *diffview.DiffView { return dv.Split() },
			width:  80,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dv := diffview.New().
				Before("main.go", TestMultipleHunksBefore).
				After("main.go", TestMultipleHunksAfter).
				Width(tt.width).
				NoTruncate()
			dv = tt.layout(dv)

			output := dv.String()

			if testing.Verbose() {
				t.Logf("Output:\n%s", output)
			}

			golden.RequireEqual(t, []byte(output))

			// Verify all lines respect width constraint.
			for i, line := range strings.Split(output, "\n") {
				w := ansi.StringWidth(line)
				if w > tt.width {
					t.Errorf("line %d exceeds width %d: got %d", i, tt.width, w)
				}
			}
		})
	}
}

// TestNoTruncateStripped renders NoTruncate diffs with ANSI stripped so
// the visual layout can be inspected in test output.
func TestNoTruncateStripped(t *testing.T) {
	before := "package main\n\nfunc example() string {\n\treturn \"short line\"\n}\n"
	after := "package main\n\nfunc example() string {\n\treturn \"this is a very long string that definitely exceeds the column width and should demonstrate wrapping behavior properly\"\n}\n"

	for _, tc := range []struct {
		name  string
		width int
		split bool
	}{
		{"unified_w60", 60, false},
		{"split_w80", 80, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dv := diffview.New().
				Before("main.go", before).
				After("main.go", after).
				Width(tc.width).
				NoTruncate()
			if tc.split {
				dv = dv.Split()
			}
			out := dv.String()
			lines := strings.Split(out, "\n")
			t.Logf("=== %s (target width=%d) ===", tc.name, tc.width)
			for i, line := range lines {
				stripped := ansi.Strip(line)
				w := ansi.StringWidth(line)
				t.Logf("  %2d (w=%3d) |%s|", i, w, stripped)
			}
			for i, line := range lines {
				w := ansi.StringWidth(line)
				if w != 0 && w != tc.width {
					t.Errorf("line %d: width=%d, want %d: %q", i, w, tc.width, ansi.Strip(line))
				}
			}
		})
	}
}

// TestNoTruncateSplitBothSidesLong verifies that when both sides of a
// split diff have long lines, each panel wraps independently.
func TestNoTruncateSplitBothSidesLong(t *testing.T) {
	t.Parallel()

	before := "package main\n\nfunc foo() {\n\tx := callSomeFunctionWithAVeryLongName(argumentOne, argumentTwo, argumentThree, argumentFour)\n}\n"
	after := "package main\n\nfunc foo() {\n\tx := callADifferentFunctionWithLongName(parameterAlpha, parameterBeta, parameterGamma, parameterDelta)\n}\n"

	dv := diffview.New().
		Before("main.go", before).
		After("main.go", after).
		Width(80).
		Split().
		NoTruncate()
	out := dv.String()
	lines := strings.Split(out, "\n")
	t.Logf("=== split_both_long_w80 ===")
	for i, line := range lines {
		stripped := ansi.Strip(line)
		w := ansi.StringWidth(line)
		t.Logf("  %2d (w=%3d) |%s|", i, w, stripped)
	}
	for i, line := range lines {
		w := ansi.StringWidth(line)
		if w != 0 && w != 80 {
			t.Errorf("line %d: width=%d, want 80: %q", i, w, ansi.Strip(line))
		}
	}
	// Verify content is NOT truncated with ellipsis. The "…" character
	// should only appear in structural elements (hunk headers, line nums).
	stripped := ansi.Strip(out)
	for i, line := range strings.Split(stripped, "\n") {
		sline := strings.TrimSpace(line)
		// Skip structural lines: hunk headers and line-number-only lines.
		if strings.Contains(sline, "@@") || sline == "…" || sline == "" {
			continue
		}
		// Check that code content doesn't end with the truncation marker.
		if strings.HasSuffix(strings.TrimSpace(sline), "…") && !strings.HasPrefix(sline, "…") {
			t.Errorf("line %d has truncation ellipsis in content: %q", i, line)
		}
	}
}

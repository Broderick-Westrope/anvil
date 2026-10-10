package attachments

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func overflowFixture() []message.Attachment {
	atts := make([]message.Attachment, 8)
	for i := range atts {
		atts[i] = message.Attachment{FileName: fmt.Sprintf("file-%d.txt", i)}
	}
	return atts
}

// chipWidth is the rendered width of a lone editor chip for the given
// filename. Below this a row cannot fit even one chip, so it is the
// natural floor for any "the row fits" assertion.
func chipWidth(r *Renderer, name string) int {
	return lipgloss.Width(r.Render([]message.Attachment{{FileName: name}}, nil, false, true, 1000))
}

// hintCount returns the number the "N more…" hint claims, or 0 when the
// row carries no hint. The trailing trim matters: a hint padded out to a
// fixed width would otherwise read as no hint at all.
func hintCount(out string) int {
	plain := strings.TrimRight(ansi.Strip(out), " ")
	if !strings.HasSuffix(plain, "more…") {
		return 0
	}
	fields := strings.Fields(plain)
	n, err := strconv.Atoi(fields[len(fields)-2])
	if err != nil {
		return 0
	}
	return n
}

func TestRender_SingleVisibleChipHasNoMoreHint(t *testing.T) {
	t.Parallel()

	// Regression: the row used to budget every chip at the maximum filename
	// width, so on a narrow editor a single short-named attachment was
	// counted as overflowing its own row. The chip rendered fine and then
	// "1 more…" was tacked on beside it, promising a file that was already
	// on screen. Widths are now measured as chips are laid out.
	r := newTestRenderer()
	atts := []message.Attachment{{FileName: "paste_1.png"}}
	for width := range 121 {
		require.Zero(t, hintCount(r.Render(atts, nil, false, true, width)),
			"a lone attachment must never claim there are others (width %d)", width)
	}
}

func TestRender_HintOnlyAppearsWhenSomethingIsHidden(t *testing.T) {
	t.Parallel()

	// The stronger form of the regression above: for any number of
	// attachments at any width, a hint may appear only when chips were
	// actually left out, and it must name exactly how many. A single-
	// attachment test cannot catch a hint that is off by one for n >= 2.
	r := newTestRenderer()
	all := overflowFixture()
	for n := 1; n <= len(all); n++ {
		for width := chipWidth(r, "file-0.txt"); width < 161; width++ {
			out := r.Render(all[:n], nil, false, true, width)
			drawn := strings.Count(out, styles.TextIcon)
			require.Positive(t, drawn, "some chip must always be drawn (n=%d width=%d)", n, width)
			if got := hintCount(out); got != 0 {
				require.Equal(t, n-drawn, got,
					"hint must name exactly the chips left out (n=%d width=%d drew=%d)", n, width, drawn)
			} else {
				require.True(t, drawn == n || drawn == 1,
					"a row may go without the hint only when everything fit, or when "+
						"a single chip crowded the hint out (n=%d width=%d drew=%d)", n, width, drawn)
			}
		}
	}
}

func TestRender_DrawnChipsAreTheLeadingRun(t *testing.T) {
	t.Parallel()

	// Overflow drops chips off the end, never the middle. Click handling
	// indexes straight into the attachment slice with the chip's position,
	// so a gap would remove the wrong file.
	r := newTestRenderer()
	atts := overflowFixture()
	for width := chipWidth(r, "file-0.txt"); width < 161; width++ {
		out := ansi.Strip(r.Render(atts, nil, false, true, width))
		drawn := strings.Count(out, styles.TextIcon)
		for i := range drawn {
			name := fmt.Sprintf("file-%d.txt", i)
			if drawn == 1 && width >= minFilename {
				require.True(t, showsName(out, name),
					"the only drawn chip must be chip %d (width %d)", i, width)
				continue
			}
			require.Contains(t, out, name,
				"chip %d must be present when %d chips were drawn (width %d)", i, drawn, width)
		}
		for i := drawn; i < len(atts); i++ {
			require.NotContains(t, out, fmt.Sprintf("file-%d.txt", i),
				"chip %d must be absent when only %d chips were drawn (width %d)", i, drawn, width)
		}
	}
}

// showsName reports whether the stripped row shows name, either whole or
// trimmed down to a leading run followed by an ellipsis.
func showsName(out, name string) bool {
	if strings.Contains(out, name) {
		return true
	}
	for n := len(name) - 1; n > 0; n-- {
		if strings.Contains(out, name[:n]+"…") {
			return true
		}
	}
	return false
}

func TestRender_MoreHintUsesTheThemeStyle(t *testing.T) {
	t.Parallel()

	// The hint used to be rendered with a bare lipgloss.Style, so it came
	// out in the terminal's default foreground next to the themed chips.
	// The expectation is built from the theme rather than from the
	// renderer, so this cannot pass by agreeing with itself.
	r := newTestRenderer()
	atts := overflowFixture()
	out := r.Render(atts, nil, false, true, 60)

	n := hintCount(out)
	require.Positive(t, n, "width 60 must overflow for this to mean anything")
	want := styles.TokyoNight().Attachments.More.Render(fmt.Sprintf("%d more…", n))
	require.Contains(t, want, "\x1b[", "the theme's hint style must actually set a color")
	require.Contains(t, out, want, "the row must render the hint through the theme style")
}

func TestRender_RowNeverSpills(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := overflowFixture()
	for width := range 161 {
		require.LessOrEqual(t, lipgloss.Width(r.Render(atts, nil, false, true, width)), width,
			"the attachment row must not spill past the editor (width %d)", width)
	}
}

func TestRender_OverflowHoldsInEveryMode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                 string
		deleting, showRemove bool
	}{
		{"editor", false, true},
		{"delete mode", true, true},
		{"posted message", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newTestRenderer()
			atts := overflowFixture()
			out := r.Render(atts, nil, tc.deleting, tc.showRemove, 60)
			drawn := strings.Count(out, styles.TextIcon)
			require.Less(t, drawn, len(atts), "width 60 must overflow for this to mean anything")
			require.Equal(t, len(atts)-drawn, hintCount(out))
			require.LessOrEqual(t, lipgloss.Width(out), 60)
		})
	}
}

func TestHitTestRemove_OverflowBoundsOnlyCoverDrawnChips(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := overflowFixture()
	out := r.Render(atts, nil, false, true, 60)
	drawn := strings.Count(out, styles.TextIcon)
	require.Less(t, drawn, len(atts), "width 60 must overflow for this to mean anything")
	require.Len(t, r.bounds, drawn, "only drawn chips may carry a clickable remove button")

	for x := range lipgloss.Width(out) {
		require.Less(t, r.HitTestRemove(atts, x), drawn,
			"column %d hit-tests to a chip that was never drawn", x)
	}
}

func TestRender_LoneChipTrimsItsNameToKeepTheHint(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := overflowFixture()
	full := chipWidth(r, "file-0.txt")

	hint := lipgloss.Width(styles.TokyoNight().Attachments.More.
		Render(fmt.Sprintf("%d more…", len(atts)-1)))
	width := full + hint - 1

	out := r.Render(atts, nil, false, true, width)
	require.Equal(t, 1, strings.Count(out, styles.TextIcon),
		"only one chip should fit at width %d", width)
	require.Equal(t, len(atts)-1, hintCount(out),
		"the hint must survive and name every file left off (width %d)", width)
	require.NotContains(t, ansi.Strip(out), "file-0.txt",
		"the name must have been trimmed to make that room")
	require.LessOrEqual(t, lipgloss.Width(out), width,
		"the trimmed row must still fit the width it was given")
}

func TestRender_NonPositiveWidthRendersNothing(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	atts := []message.Attachment{{FileName: "a.txt"}, {FileName: "b.txt"}}
	for _, width := range []int{-5, 0} {
		require.Empty(t, r.Render(atts, nil, false, true, width))
		require.Empty(t, r.bounds)
	}
}

func TestRender_NoAttachmentsRendersNothing(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	require.Empty(t, r.Render(nil, nil, false, true, 80))
	require.Empty(t, r.bounds)
}

func textFile(name string) message.Attachment {
	return message.Attachment{FileName: name, MimeType: "text/plain"}
}

// TestRender_FitsEveryChipThatFits pins the rows that used to hide chips
// with room to spare: every chip was budgeted at the widest filename, so
// short names were dropped behind "N more…" well before the row was full.
func TestRender_FitsEveryChipThatFits(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	for _, tc := range []struct {
		files []message.Attachment
		width int
	}{
		{[]message.Attachment{textFile("a.go"), textFile("b.go")}, 60},
		{[]message.Attachment{textFile("a.go"), textFile("b.go"), textFile("c.go"), textFile("d.go"), textFile("e.go")}, 100},
	} {
		out := r.Render(tc.files, nil, false, true, tc.width)
		require.Equal(t, len(tc.files), strings.Count(out, styles.TextIcon), "width %d: %q", tc.width, ansi.Strip(out))
		require.Zero(t, hintCount(out))
	}
}

func TestRender_SkillsShareTheRowAndTheCount(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	skills := []SkillAttachment{{Name: "go-style"}, {Name: "writer"}}

	t.Run("a file and a skill that fit are both drawn", func(t *testing.T) {
		t.Parallel()
		out := ansi.Strip(r.Render([]message.Attachment{textFile("a.go")}, skills[:1], false, true, 60))
		require.Contains(t, out, "a.go")
		require.Contains(t, out, "go-style")
		require.Zero(t, hintCount(out))
	})

	t.Run("skills left out are counted in the hint", func(t *testing.T) {
		t.Parallel()
		files := overflowFixture()[:2]
		width := chipWidth(r, "file-0.txt")*2 + lipgloss.Width(r.more(2))
		out := r.Render(files, skills, false, true, width)
		require.Equal(t, 2, strings.Count(out, styles.TextIcon))
		require.NotContains(t, ansi.Strip(out), "go-style")
		require.Equal(t, 2, hintCount(out))
		require.LessOrEqual(t, lipgloss.Width(out), width)
	})

	t.Run("skills alone overflow with a hint", func(t *testing.T) {
		t.Parallel()
		many := []SkillAttachment{{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"}}
		out := r.Render(nil, many, false, true, 25)
		require.Contains(t, ansi.Strip(out), "one")
		require.Positive(t, hintCount(out))
		require.LessOrEqual(t, lipgloss.Width(out), 25)
	})

	t.Run("delete mode numbers skills after files", func(t *testing.T) {
		t.Parallel()
		out := ansi.Strip(r.Render([]message.Attachment{textFile("a.go")}, skills[:1], true, true, 80))
		require.Equal(t, []string{"≡", "a.go", "0", "1", "go-style"}, strings.Fields(out))
	})

	t.Run("skill chips carry no remove button", func(t *testing.T) {
		t.Parallel()
		_ = r.Render(nil, skills, false, true, 80)
		require.Empty(t, r.bounds)
	})
}

func TestRender_RowNeverSpillsWithLongNamesAndSkills(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	files := []message.Attachment{textFile("a_really_long_file_name_here.go"), textFile("another_long_name_for_file.go")}
	skills := []SkillAttachment{{Name: "a-skill-with-a-very-long-name"}}
	for width := range 161 {
		require.LessOrEqual(t, lipgloss.Width(r.Render(files, skills, false, true, width)), width, "width %d", width)
	}
}

func TestRender_MoreHintIsNotPadded(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	out := ansi.Strip(r.Render(overflowFixture(), nil, false, true, 60))
	require.True(t, strings.HasSuffix(out, "more…"), "hint must end the row with no padding: %q", out)
}

func TestRender_LastChipNeedsNoRoomForAHint(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	files := overflowFixture()[:3]
	width := lipgloss.Width(r.Render(files, nil, false, true, 1000))

	out := r.Render(files, nil, false, true, width)
	require.Equal(t, len(files), strings.Count(out, styles.TextIcon),
		"a row exactly as wide as its chips must draw them all: %q", ansi.Strip(out))
	require.Zero(t, hintCount(out))
}

func TestRender_LoneChipKeepsItsNameAboveTheFloor(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	sty := styles.TokyoNight().Attachments
	files := []message.Attachment{textFile("abcdefghijklmnop.txt"), textFile("b.txt")}
	lead := lipgloss.Width(sty.Text.String())
	trail := lipgloss.Width(sty.Remove.String())
	hint := lipgloss.Width(r.more(1))
	frame := sty.Normal.GetHorizontalFrameSize()

	// One column short of the floor: trimming would leave a name of
	// minFilename-1 cells, so the hint gives way instead.
	width := lead + trail + hint + frame + minFilename - 1
	out := r.Render(files, nil, false, true, width)
	require.Zero(t, hintCount(out), "%q", ansi.Strip(out))

	// At the floor the name is trimmed and the hint stays.
	out = r.Render(files, nil, false, true, width+1)
	require.Equal(t, 1, hintCount(out), "%q", ansi.Strip(out))
}

func TestHitTestRemove_ClippedButtonIsNotClickable(t *testing.T) {
	t.Parallel()

	r := newTestRenderer()
	files := []message.Attachment{textFile("a_really_long_file_name_here.go")}
	width := chipWidth(r, "a_really_long_file_name_here.go") - 1

	_ = r.Render(files, nil, false, true, width)
	require.Empty(t, r.bounds, "a remove button cut off the row must not be clickable")
}

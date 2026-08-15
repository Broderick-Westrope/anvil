package dialog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// filePickerTotalHeight reconstructs the rendered dialog height from the
// helper's output: borders (2) + title (1) + gap (1) + optional preview and
// its trailing gap + file list + gap (1) + help (1).
func filePickerTotalHeight(listHeight, previewHeight int) int {
	total := 2 + 1 + 1 + listHeight + 1 + 1
	if previewHeight > 0 {
		total += previewHeight + 1
	}
	return total
}

// TestFilePickerHeights_FitsArea verifies the dialog never renders taller
// than the area it is given. Regression for the preview being a fixed 20
// rows regardless of terminal height, which clipped the dialog's bottom
// border and help line on terminals under ~37 rows.
func TestFilePickerHeights_FitsArea(t *testing.T) {
	t.Parallel()

	for areaHeight := 6; areaHeight <= 60; areaHeight++ {
		listHeight, previewHeight := filePickerHeights(areaHeight, 2, 0)
		total := filePickerTotalHeight(listHeight, previewHeight)
		require.LessOrEqualf(t, total, areaHeight,
			"dialog height %d exceeds area height %d (list=%d preview=%d)",
			total, areaHeight, listHeight, previewHeight)
		require.GreaterOrEqual(t, listHeight, 0)
		require.GreaterOrEqual(t, previewHeight, 0)
	}
}

// TestFilePickerHeights_FullSizeOnLargeScreens verifies large terminals
// still get the full-size preview and file list.
func TestFilePickerHeights_FullSizeOnLargeScreens(t *testing.T) {
	t.Parallel()

	listHeight, previewHeight := filePickerHeights(50, 2, 0)
	require.Equal(t, 10, listHeight)
	require.Equal(t, filePickerMinHeight*2, previewHeight)
}

// TestFilePickerHeights_PreviewScalesThenHides verifies the preview
// shrinks to fit mid-size terminals and disappears (rather than rendering
// a sliver) when there is no useful room for it.
func TestFilePickerHeights_PreviewScalesThenHides(t *testing.T) {
	t.Parallel()

	// 30-row terminal: preview must shrink below full size but survive.
	listHeight, previewHeight := filePickerHeights(30, 2, 0)
	require.Equal(t, 10, listHeight)
	require.Positive(t, previewHeight)
	require.Less(t, previewHeight, filePickerMinHeight*2)

	// Short terminal: preview hides entirely, file list keeps priority.
	listHeight, previewHeight = filePickerHeights(16, 2, 0)
	require.Zero(t, previewHeight)
	require.Equal(t, 10, listHeight)

	// Tiny terminal: even the file list shrinks.
	listHeight, previewHeight = filePickerHeights(10, 2, 0)
	require.Zero(t, previewHeight)
	require.Equal(t, 4, listHeight)
}

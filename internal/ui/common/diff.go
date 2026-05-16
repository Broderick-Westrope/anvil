package common

import (
	"github.com/Broderick-Westrope/anvil/internal/ui/diffview"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/alecthomas/chroma/v2"
)

// DiffFormatter returns a diff formatter with the given styles that can be
// used to format diff outputs.
func DiffFormatter(s *styles.Styles) *diffview.DiffView {
	formatDiff := diffview.New()
	style := chroma.MustNewStyle("anvil", s.ChromaTheme())
	diff := formatDiff.ChromaStyle(style).Style(s.Diff).TabWidth(4)
	return diff
}

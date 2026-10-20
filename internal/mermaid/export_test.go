package mermaid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTMLDocument(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	res, err := r.Render(spikeDiagrams["nested"], Options{})
	require.NoError(t, err)

	doc, err := HTMLDocument(res, testTheme, "Diagram <1>")
	require.NoError(t, err)
	require.Contains(t, doc, "<title>Diagram &lt;1&gt;</title>")
	require.Contains(t, doc, "background:"+testTheme["pele-bg"])
	require.Equal(t, len(fontFaces), strings.Count(doc, "@font-face"))
	require.Contains(t, doc, `<svg xmlns="http://www.w3.org/2000/svg" class="pele`)
	require.NotContains(t, doc, "var(--pele-", "theme colours are resolved")

	if dir := os.Getenv("MERMAID_SPIKE_OUT"); dir != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "nested.html"), []byte(doc), 0o644))
	}
}

func TestThemeWith(t *testing.T) {
	t.Parallel()

	base := Theme{"pele-bg": "#000", "pele-fg": "#fff"}
	got := base.With("pele-bg", "#123")
	require.Equal(t, Theme{"pele-bg": "#123", "pele-fg": "#fff"}, got)
	require.Equal(t, "#000", base["pele-bg"], "the original is unchanged")
}

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
	res, err := r.Render(sampleDiagrams["nested"], Options{})
	require.NoError(t, err)

	doc, err := HTMLDocument(res, testTheme, "Diagram <1>")
	require.NoError(t, err)
	require.Contains(t, doc, "<title>Diagram &lt;1&gt;</title>")
	require.Contains(t, doc, "background:"+testTheme["pele-bg"])
	require.Equal(t, len(fontFaces), strings.Count(doc, "@font-face"))
	require.Contains(t, doc, `<svg xmlns="http://www.w3.org/2000/svg" class="pele`)
	require.NotContains(t, doc, "var(--pele-", "theme colours are resolved")

	if dir := os.Getenv("MERMAID_TEST_OUT"); dir != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "nested.html"), []byte(doc), 0o644))
	}
}

func TestHTMLDocumentResolvesVariables(t *testing.T) {
	t.Parallel()

	svg := `<svg style="--_bg:var(--pele-bg,#fff);--_fg:var(--pele-fg,#000)" fill="var(--_fg)"><rect fill="var(--_bg)" stroke="var(--missing, var(--_fg))"/></svg>`
	doc, err := HTMLDocument(Result{SVG: svg}, Theme{"pele-bg": "#111"}, "d")
	require.NoError(t, err)
	require.Contains(t, doc, `<svg style="--_bg:#111;--_fg:#000" fill="#000"><rect fill="#111" stroke="#000"/></svg>`)
}

func TestHTMLDocumentKeepsLabelsVerbatim(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	res, err := r.Render(`flowchart LR
  A["var(--pele-bg)"] --> B["--_s: red"] --> C["var(--nope, red)"]`, Options{})
	require.NoError(t, err)

	doc, err := HTMLDocument(res, testTheme, "d")
	require.NoError(t, err)
	svg := doc[strings.Index(doc, "<svg"):]
	require.Contains(t, svg, ">var(--pele-bg)<")
	require.Contains(t, svg, ">--_s: red<")
	require.Contains(t, svg, ">var(--nope, red)<")
	require.Contains(t, svg, `fill="`+testTheme["pele-surface"]+`"`, "a label can't redeclare a variable")
}

func TestHTMLDocumentRejectsExplodingVariables(t *testing.T) {
	t.Parallel()

	doc, err := HTMLDocument(Result{SVG: explodingVarsSVG}, testTheme, "d")
	require.ErrorContains(t, err, "too large", "got %d bytes", len(doc))
}

func TestHTMLDocumentBlocksRemoteLoads(t *testing.T) {
	t.Parallel()

	doc, err := HTMLDocument(Result{SVG: `<svg xmlns="http://www.w3.org/2000/svg"></svg>`}, testTheme, "d")
	require.NoError(t, err)
	require.Contains(t, doc, `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; font-src data:; img-src data:">`)
}

package mermaid

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/pele-go/raster"
	"github.com/stretchr/testify/require"
)

// testTheme is a dark palette for tests. Anvil's real one comes from
// internal/ui/styles.
var testTheme = raster.Theme{
	"pele-bg":      "#101010",
	"pele-fg":      "#f0f0f0",
	"pele-line":    "#c0c0c0",
	"pele-surface": "#303030",
	"pele-border":  "#a080f0",
	"pele-accent":  "#7090f0",
}

const nestedDiagram = `flowchart TB
  subgraph app [internal/app]
    subgraph agent [internal/agent]
      coordinator[Coordinator] --> session[SessionAgent]
    end
    workflowhost --> coordinator
  end
  ui[internal/ui] --> workflowhost`

// explodingVarsSVG declares a chain of variables that each use the next
// twice, so substituting them doubles at every link. Twenty links expand
// to about 4 MB, past the 1 MiB cap but small enough that a test run with
// the cap broken doesn't exhaust memory.
var explodingVarsSVG = func() string {
	const links = 20
	var decls strings.Builder
	for i := range links {
		fmt.Fprintf(&decls, "--v%d:var(--v%d)var(--v%d);", i, i+1, i+1)
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" style="%s--v%d:#fff"><rect width="10" height="10" fill="var(--v0)"/></svg>`, decls.String(), links)
}()

func TestHTMLDocument(t *testing.T) {
	t.Parallel()

	r, err := raster.NewRenderer()
	require.NoError(t, err)
	res, err := r.Render(t.Context(), nestedDiagram, raster.Options{})
	require.NoError(t, err)

	doc, err := HTMLDocument(res, testTheme, "Diagram <1>")
	require.NoError(t, err)
	require.Contains(t, doc, "<title>Diagram &lt;1&gt;</title>")
	require.Contains(t, doc, "background:"+testTheme["pele-bg"])
	require.Equal(t, len(raster.FontFaces()), strings.Count(doc, "@font-face"))
	require.Contains(t, doc, `<svg xmlns="http://www.w3.org/2000/svg" class="pele`)
	require.NotContains(t, doc, "var(--pele-", "theme colours are resolved")

	if dir := os.Getenv("MERMAID_TEST_OUT"); dir != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "nested.html"), []byte(doc), 0o644))
	}
}

func TestHTMLDocumentResolvesVariables(t *testing.T) {
	t.Parallel()

	svg := `<svg style="--_bg:var(--pele-bg,#fff);--_fg:var(--pele-fg,#000)" fill="var(--_fg)"><rect fill="var(--_bg)" stroke="var(--missing, var(--_fg))"/></svg>`
	doc, err := HTMLDocument(raster.Result{SVG: svg}, raster.Theme{"pele-bg": "#111"}, "d")
	require.NoError(t, err)
	require.Contains(t, doc, `<svg style="--_bg:#111;--_fg:#000" fill="#000"><rect fill="#111" stroke="#000"/></svg>`)
}

func TestHTMLDocumentKeepsLabelsVerbatim(t *testing.T) {
	t.Parallel()

	r, err := raster.NewRenderer()
	require.NoError(t, err)
	res, err := r.Render(t.Context(), `flowchart LR
  A["var(--pele-bg)"] --> B["--_s: red"] --> C["var(--nope, red)"]`, raster.Options{})
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

	doc, err := HTMLDocument(raster.Result{SVG: explodingVarsSVG}, testTheme, "d")
	require.ErrorContains(t, err, "too large", "got %d bytes", len(doc))
}

func TestHTMLDocumentBlocksRemoteLoads(t *testing.T) {
	t.Parallel()

	doc, err := HTMLDocument(raster.Result{SVG: `<svg xmlns="http://www.w3.org/2000/svg"></svg>`}, testTheme, "d")
	require.NoError(t, err)
	require.Contains(t, doc, `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; font-src data:; img-src data:">`)
}

// A label is text, so a declaration written in one is neither collected
// nor expanded. The label is short so that code which wrongly expanded it
// still fails quickly rather than exhausting memory.
func TestHTMLDocumentLabelDeclaringVariablesStaysSmall(t *testing.T) {
	t.Parallel()

	r, err := raster.NewRenderer()
	require.NoError(t, err)
	res, err := r.Render(t.Context(), `flowchart LR
  A["--zz:var(--zz)var(--zz)"] --> B`, raster.Options{})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := HTMLDocument(res, testTheme, "d")
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("resolving the label's variables did not finish")
	}
}

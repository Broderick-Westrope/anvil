package mermaid

import (
	"encoding/base64"
	"fmt"
	"html"
	"strings"
)

// exportFontFamily names the embedded DejaVu Sans in exported pages, so the
// browser draws labels with the font Pele measured them in.
const exportFontFamily = "Anvil DejaVu Sans"

// fontFaces maps each embedded face to its CSS weight and style.
var fontFaces = []struct {
	path, weight, style string
}{
	{"fonts/DejaVuSans.ttf", "normal", "normal"},
	{"fonts/DejaVuSans-Bold.ttf", "bold", "normal"},
	{"fonts/DejaVuSans-Oblique.ttf", "normal", "italic"},
	{"fonts/DejaVuSans-BoldOblique.ttf", "bold", "italic"},
}

// HTMLDocument returns a standalone page showing the diagram on the theme's
// background, scaled to the window and zoomable in the browser. It embeds
// the label fonts so the layout matches the terminal rendering.
func HTMLDocument(res Result, theme Theme, title string) (string, error) {
	var faces strings.Builder
	for _, f := range fontFaces {
		data, err := fontFiles.ReadFile(f.path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", f.path, err)
		}
		fmt.Fprintf(&faces, "@font-face{font-family:%q;font-weight:%s;font-style:%s;src:url(data:font/ttf;base64,%s) format(\"truetype\")}\n",
			exportFontFamily, f.weight, f.style, base64.StdEncoding.EncodeToString(data))
	}
	bg := theme["pele-bg"]
	fg := theme["pele-fg"]
	svg := ResolveVars(res.SVG, theme)
	return `<!doctype html>
<html><head><meta charset="utf-8"><title>` + html.EscapeString(title) + `</title>
<style>
` + faces.String() + `html,body{margin:0;background:` + bg + `;color:` + fg + `}
body{display:flex;justify-content:center;padding:32px;box-sizing:border-box;min-height:100vh}
svg.pele{max-width:100%;height:auto}
svg.pele,svg.pele text{font-family:"` + exportFontFamily + `",sans-serif !important}
</style></head><body>
` + svg + `
</body></html>
`, nil
}

package mermaid

import _ "embed"

// These are the same DejaVu Sans subsets baked into resvg/resvg.wasm, so
// Pele lays labels out with the widths resvg will actually draw.
var (
	//go:embed fonts/DejaVuSans.ttf
	fontRegular []byte
	//go:embed fonts/DejaVuSans-Bold.ttf
	fontBold []byte
	//go:embed fonts/DejaVuSans-Oblique.ttf
	fontItalic []byte
	//go:embed fonts/DejaVuSans-BoldOblique.ttf
	fontBoldItalic []byte
)

// fontFaces lists the embedded faces with their CSS weight and style.
var fontFaces = [4]struct {
	data          []byte
	weight, style string
}{
	{fontRegular, "normal", "normal"},
	{fontBold, "bold", "normal"},
	{fontItalic, "normal", "italic"},
	{fontBoldItalic, "bold", "italic"},
}

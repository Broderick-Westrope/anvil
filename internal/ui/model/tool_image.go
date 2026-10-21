package model

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"image"
	"image/draw"
	_ "image/gif"  // Registers the GIF decoder for tool images.
	_ "image/jpeg" // Registers the JPEG decoder for tool images.
	_ "image/png"  // Registers the PNG decoder for tool images.
	"math"

	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // Registers the WebP decoder for tool images.
)

// FullImage implements chat.ImageRenderer.
func (f *mermaidFeature) FullImage(data, mimeType string, width int) chat.DiagramView {
	cellW, cellH := f.caps.CellSize()
	if !f.caps.SupportsKittyGraphics() || cellW == 0 || cellH == 0 || width <= 0 {
		return chat.DiagramView{State: chat.DiagramUnsupported}
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(data))
	key := diagramKey{producer: producerBitmap, hash: h.Sum64(), width: width, kind: diagramFull, cellW: cellW, cellH: cellH}
	req := diagramRequest{key: key, data: data, mimeType: mimeType, cellW: cellW, cellH: cellH, maxCols: width}
	return f.lookupRequest(req, true)
}

// renderBitmap decodes req's image and fits it to the drill-in: actual
// size, one image pixel per screen pixel, shrunk to fit but never
// enlarged, and padded to whole cells.
func renderBitmap(req diagramRequest) diagramReadyMsg {
	fail := func(err error) diagramReadyMsg { return diagramReadyMsg{key: req.key, err: err} }
	raw, err := base64.StdEncoding.DecodeString(req.data)
	if err != nil {
		return fail(fmt.Errorf("decode image data: %w", err))
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return fail(fmt.Errorf("decode image: %w", err))
	}
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	cw, ch := float64(req.cellW), float64(req.cellH)
	scale := math.Min(1, float64(req.maxCols)*cw/w)
	scale = math.Min(scale, diagramMaxFullRows*ch/h)
	scale = math.Min(scale, math.Sqrt(diagramFullMaxPixels/(w*h)))
	if scale < 1 {
		img = imaging.Resize(img, max(1, int(w*scale)), max(1, int(h*scale)), imaging.Lanczos)
		b = img.Bounds()
	}
	cols := int(math.Ceil(float64(b.Dx()) / cw))
	rows := int(math.Ceil(float64(b.Dy()) / ch))
	padded := image.NewRGBA(image.Rect(0, 0, cols*req.cellW, rows*req.cellH))
	draw.Draw(padded, b.Sub(b.Min), img, b.Min, draw.Src)
	return diagramReadyMsg{key: req.key, img: padded, cols: cols, rows: rows, cellW: req.cellW, cellH: req.cellH}
}

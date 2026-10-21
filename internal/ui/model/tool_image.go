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
	"mime"
	"os"
	"path/filepath"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // Registers the WebP decoder for tool images.
)

// FullImage implements chat.ImageRenderer, for when no drill-in is open;
// drill-ins ask through ImagesFor.
func (f *mermaidFeature) FullImage(data, mimeType string, width int) chat.DiagramView {
	return f.fullImage(nil, data, mimeType, width)
}

// fullImage is a tool image as owner's drill-in shows it. It only renders
// while owner is on top.
func (f *mermaidFeature) fullImage(owner *Chat, data, mimeType string, width int) chat.DiagramView {
	cellW, cellH := f.caps.CellSize()
	if !f.caps.SupportsKittyGraphics() || cellW == 0 || cellH == 0 || width <= 0 {
		return chat.DiagramView{State: chat.DiagramUnsupported}
	}
	if owner != f.top {
		return chat.DiagramView{State: chat.DiagramPending}
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(data))
	key := diagramKey{producer: producerBitmap, hash: h.Sum64(), width: width, kind: diagramFull, cellW: cellW, cellH: cellH, gen: f.gen}
	req := diagramRequest{key: key, data: data, mimeType: mimeType, cellW: cellW, cellH: cellH, maxCols: width}
	return f.lookupRequest(req, true)
}

// Bitmap limits. A source past them isn't decoded: decoding allocates
// its full RGBA before it is shrunk to fit.
const (
	bitmapMaxSide         = 8192
	bitmapMaxSourcePixels = 32 << 20
)

// bitmapTypes are the image types tool images are decoded from.
var bitmapTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// undisplayableError is an image Anvil doesn't try to show. It is
// expected, unlike a decode failure, so it isn't worth a warning.
type undisplayableError struct{ reason string }

func (e undisplayableError) Error() string { return e.reason }

// renderBitmap decodes req's image and fits it to the drill-in: actual
// size, one image pixel per screen pixel, shrunk to fit but never
// enlarged, and padded to whole cells.
func renderBitmap(req diagramRequest) diagramReadyMsg {
	fail := func(err error) diagramReadyMsg { return diagramReadyMsg{key: req.key, err: err} }
	if !bitmapTypes[req.mimeType] {
		return fail(undisplayableError{"unsupported image type " + req.mimeType})
	}
	raw, err := base64.StdEncoding.DecodeString(req.data)
	if err != nil {
		return fail(fmt.Errorf("decode image data: %w", err))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fail(fmt.Errorf("decode image: %w", err))
	}
	if cfg.Width > bitmapMaxSide || cfg.Height > bitmapMaxSide || cfg.Width*cfg.Height > bitmapMaxSourcePixels {
		return fail(undisplayableError{fmt.Sprintf("image too large (%d×%d)", cfg.Width, cfg.Height)})
	}
	// For an animated GIF this is the first frame.
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return fail(fmt.Errorf("decode image: %w", err))
	}
	fit := fitBitmap(img.Bounds().Dx(), img.Bounds().Dy(), req)
	if fit.w != img.Bounds().Dx() || fit.h != img.Bounds().Dy() {
		img = imaging.Resize(img, fit.w, fit.h, imaging.Lanczos)
	}
	b := img.Bounds()
	padded := image.NewRGBA(image.Rect(0, 0, fit.cols*req.cellW, fit.rows*req.cellH))
	draw.Draw(padded, b.Sub(b.Min), img, b.Min, draw.Src)
	return diagramReadyMsg{key: req.key, img: padded, cols: fit.cols, rows: fit.rows, cellW: req.cellW, cellH: req.cellH}
}

// bitmapFit is the size a bitmap is drawn at, in pixels and in cells.
type bitmapFit struct {
	w, h, cols, rows int
}

// fitBitmap fits a w×h bitmap within req's width, the drill-in's rows and,
// once padded to whole cells, its pixels.
func fitBitmap(w, h int, req diagramRequest) bitmapFit {
	cw, ch := float64(req.cellW), float64(req.cellH)
	fw, fh := float64(w), float64(h)
	scale := math.Min(1, float64(req.maxCols)*cw/fw)
	scale = math.Min(scale, diagramMaxFullRows*ch/fh)
	scale = math.Min(scale, math.Sqrt(diagramFullMaxPixels/(fw*fh)))
	for {
		fit := bitmapFit{w: max(1, int(fw*scale)), h: max(1, int(fh*scale))}
		fit.cols = int(math.Ceil(float64(fit.w) / cw))
		fit.rows = int(math.Ceil(float64(fit.h) / ch))
		padded := fit.cols * req.cellW * fit.rows * req.cellH
		if padded <= diagramFullMaxPixels {
			return fit
		}
		scale *= math.Sqrt(diagramFullMaxPixels/float64(padded)) * 0.999
	}
}

// imageExts are the file extensions tool images are written with, so the
// OS opens them in its viewer. Types missing here fall back to the OS's
// own table.
var imageExts = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
	"image/avif":    ".avif",
	"image/bmp":     ".bmp",
}

// openImage writes a tool image's bytes, exactly as the model saw them, to
// the export directory and opens them in the OS default viewer. It works
// for images Anvil can't draw, since the viewer may.
func (f *mermaidFeature) openImage(msg chat.ImageOpenMsg) tea.Cmd {
	dir, open := f.exportDir, f.openURL
	return func() tea.Msg {
		path, err := writeImage(dir, msg)
		if err == nil {
			err = open(fileURL(path))
		}
		if err != nil {
			return util.NewErrorMsg(fmt.Errorf("open image: %w", err))
		}
		return util.NewInfoMsg("Opened image in viewer")
	}
}

func writeImage(dir string, msg chat.ImageOpenMsg) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		return "", fmt.Errorf("decode image data: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	pruneExports(dir)
	ext, ok := imageExts[msg.MIMEType]
	if !ok {
		if exts, _ := mime.ExtensionsByType(msg.MIMEType); len(exts) > 0 {
			ext = slices.Min(exts)
		}
	}
	h := fnv.New64a()
	_, _ = h.Write(raw)
	path := filepath.Join(dir, fmt.Sprintf("image-%x%s", h.Sum64(), ext))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

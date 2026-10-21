package model

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"  // Registers the GIF decoder for tool images.
	_ "image/jpeg" // Registers the JPEG decoder for tool images.
	_ "image/png"  // Registers the PNG decoder for tool images.
	"math"
	"net/http"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/disintegration/imaging"
	"github.com/zeebo/xxh3"
	_ "golang.org/x/image/webp" // Registers the WebP decoder for tool images.
)

// fullImage is a tool image as owner's drill-in shows it. It only renders
// while owner is on top.
func (f *graphicsFeature) fullImage(owner *Chat, data, mimeType string, width int) chat.DiagramView {
	cellW, cellH, ok := f.drawable(width)
	if !ok {
		return chat.DiagramView{State: chat.DiagramUnsupported}
	}
	if owner != f.top {
		return chat.DiagramView{State: chat.DiagramPending}
	}
	// HashString reads the string in place: the data can be megabytes, and
	// this runs on every render.
	key := diagramKey{producer: producerBitmap, hash: xxh3.HashString(data), width: width, kind: diagramFull, cellW: cellW, cellH: cellH}
	req := diagramRequest{key: key, data: data, mimeType: mimeType, cellW: cellW, cellH: cellH, maxCols: width}
	return f.lookupRequest(req, true)
}

// Bitmap limits. A source past them isn't decoded: decoding allocates
// all its pixels before it is shrunk to fit.
const (
	bitmapMaxSide         = 8192
	bitmapMaxPixels       = 32 << 20
	bitmapMaxDecodedBytes = 128 << 20
)

// decodeBytesPerPixel is the most a decoder of format holds at once per
// pixel. Progressive JPEG keeps a 256-byte coefficient block for every
// 8×8 block of every component, up to four, besides the image, and CMYK
// JPEG its four planes besides the CMYK image; lossless WebP builds the
// image through whole-image buffers of four bytes a pixel, one per
// transform step; interlaced PNG holds the image and a pass. Otherwise
// the decoders write straight into the image.
func decodeBytesPerPixel(format string, m color.Model, raw []byte) int {
	switch format {
	case "jpeg":
		if jpegProgressive(raw) {
			return 20
		}
		if m == color.CMYKModel {
			return 8
		}
	case "webp":
		return 12
	case "png":
		if pngInterlaced(raw) {
			return 2 * decodedBytesPerPixel(m)
		}
	}
	return decodedBytesPerPixel(m)
}

// pngInterlaced reports whether the PNG in raw is interlaced, from its
// IHDR, which always comes first.
func pngInterlaced(raw []byte) bool {
	const interlaceAt = 8 + 8 + 12 // Signature, chunk header, then the field.
	return len(raw) > interlaceAt && raw[interlaceAt] != 0
}

// jpegProgressive reports whether the JPEG in raw is progressive, from its
// frame header. A header it can't find counts as progressive, the costlier
// case.
func jpegProgressive(raw []byte) bool {
	i := 2 // After the start-of-image marker.
	for i+4 <= len(raw) {
		if raw[i] != 0xff {
			return true
		}
		marker := raw[i+1]
		switch marker {
		case 0xff:
			i++ // A fill byte.
			continue
		case 0xc2:
			return true
		case 0xc0, 0xc1:
			return false
		case 0xda:
			// The scan started before any frame header.
			return true
		}
		i += 2 + (int(raw[i+2])<<8 | int(raw[i+3]))
	}
	return true
}

// decodedBytesPerPixel is the most the decoders allocate per pixel for an
// image of model m. A 16-bit PNG takes twice an 8-bit one, and a grey
// PNG with a transparent colour decodes to RGBA of its depth, which
// DecodeConfig can't tell. Anything else but a palette is taken as RGBA,
// which is at least what JPEG and WebP use.
func decodedBytesPerPixel(m color.Model) int {
	switch m {
	case color.RGBA64Model, color.NRGBA64Model, color.Gray16Model:
		return 8
	}
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	return 4
}

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
		// The type is the tool's to choose, so it stays off the screen.
		return fail(undisplayableError{"unsupported image type"})
	}
	raw, err := base64.StdEncoding.DecodeString(req.data)
	if err != nil {
		return fail(fmt.Errorf("decode image data: %w", err))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fail(fmt.Errorf("decode image: %w", err))
	}
	pixels := cfg.Width * cfg.Height
	if cfg.Width > bitmapMaxSide || cfg.Height > bitmapMaxSide || pixels > bitmapMaxPixels ||
		pixels*decodeBytesPerPixel(format, cfg.ColorModel, raw) > bitmapMaxDecodedBytes {
		return fail(undisplayableError{fmt.Sprintf("image too large (%d×%d)", cfg.Width, cfg.Height)})
	}
	if req.cellW*req.cellH > diagramFullMaxPixels {
		// Not even one cell fits, so fitBitmap would never finish.
		return fail(undisplayableError{"terminal cells too large to draw in"})
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
// once padded to whole cells, its pixels. One cell must fit those pixels.
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

// imageExts are the image types o opens, by the type their bytes sniff as,
// and the extensions they are written with so the OS opens them in its
// viewer. They are raster types that carry no code: a tool result is
// untrusted, and the OS opens types such as SVG or HTML in a browser,
// which would run scripts in them. The bytes decide, not the result's
// MIME type, which the tool chose and some openers (xdg-open) ignore.
var imageExts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
	"image/bmp":  ".bmp",
}

// openImage writes a tool image's bytes, exactly as the model saw them, to
// the export directory and opens them in the OS default viewer. It works
// for images Anvil can't draw, since the viewer may.
func (f *graphicsFeature) openImage(msg chat.ImageOpenMsg) tea.Cmd {
	dir, open := f.exportDir, f.openURL
	return func() tea.Msg {
		fail := func(err error) tea.Msg { return util.NewErrorMsg(fmt.Errorf("open image: %w", err)) }
		raw, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			return fail(fmt.Errorf("decode image data: %w", err))
		}
		ext, ok := imageExts[http.DetectContentType(raw)]
		if !ok {
			return util.NewWarnMsg("Won't open this image: it isn't a known raster image, which can't run code")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(fmt.Errorf("create export dir: %w", err))
		}
		pruneExports(dir)
		path := filepath.Join(dir, fmt.Sprintf("image-%x%s", xxh3.Hash(raw), ext))
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return fail(fmt.Errorf("write image: %w", err))
		}
		if err := open(fileURL(path)); err != nil {
			return fail(fmt.Errorf("open viewer: %w", err))
		}
		return util.NewInfoMsg("Opened image in viewer")
	}
}

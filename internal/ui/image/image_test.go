package image

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

func TestResetCache(t *testing.T) {
	t.Parallel()

	cachedMutex.Lock()
	cachedImages[imageKey{id: "a", cols: 10, rows: 10}] = cachedImage{
		img:  image.NewRGBA(image.Rect(0, 0, 1, 1)),
		cols: 10,
		rows: 10,
	}
	cachedImages[imageKey{id: "b", cols: 20, rows: 20}] = cachedImage{
		img:  image.NewRGBA(image.Rect(0, 0, 1, 1)),
		cols: 20,
		rows: 20,
	}
	cachedMutex.Unlock()

	ResetCache()

	cachedMutex.RLock()
	length := len(cachedImages)
	cachedMutex.RUnlock()

	require.Equal(t, 0, length)
}

func TestResetIdempotent(t *testing.T) {
	t.Parallel()

	// Calling Reset on an empty cache should not panic.
	ResetCache()

	cachedMutex.RLock()
	length := len(cachedImages)
	cachedMutex.RUnlock()

	require.Equal(t, 0, length)
}

func TestKittyTransmitKeepsNoPixels(t *testing.T) {
	t.Parallel()

	const id = "kitty-keeps-no-pixels"
	img := image.NewRGBA(image.Rect(0, 0, 20, 40))
	msg := EncodingKitty.Transmit(id, img, CellSize{Width: 10, Height: 20}, 2, 2, false)()
	require.IsType(t, tea.RawMsg{}, msg)

	cachedMutex.RLock()
	cached, ok := cachedImages[imageKey{id: id, cols: 2, rows: 2}]
	cachedMutex.RUnlock()
	require.True(t, ok, "it is recorded as sent")
	require.Nil(t, cached.img, "the terminal holds the image, so its pixels aren't kept")
	require.True(t, HasTransmitted(id, 2, 2))
	require.NotEmpty(t, EncodingKitty.Render(id, 2, 2), "placeholders still render")
}

func TestKittyTransmitTwiceForOneKeySendsItOnce(t *testing.T) {
	t.Parallel()

	const id = "kitty-transmit-twice"
	img := image.NewRGBA(image.Rect(0, 0, 20, 40))
	cs := CellSize{Width: 10, Height: 20}
	first := EncodingKitty.Transmit(id, img, cs, 2, 2, false)
	second := EncodingKitty.Transmit(id, img, cs, 2, 2, false)
	require.IsType(t, tea.RawMsg{}, first())
	require.NotPanics(t, func() { require.Nil(t, second(), "already sent") })
}

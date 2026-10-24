package image

import (
	"fmt"
	"image"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The reset tests clear the shared cache, so they run before the parallel
// tests that fill it.
//
//nolint:paralleltest // Clears the package-level cache.
func TestResetCache(t *testing.T) {
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

//nolint:paralleltest // Clears the package-level cache.
func TestResetCacheKeepsImagesTheTerminalHolds(t *testing.T) {
	const id = "reset-keeps-sent"
	img := image.NewRGBA(image.Rect(0, 0, 20, 40))
	EncodingKitty.Transmit(id, img, CellSize{Width: 10, Height: 20}, 2, 2, false)()

	ResetCache()
	require.True(t, HasTransmitted(id, 2, 2), "it holds no pixels, and the terminal still has it")
	require.NotEmpty(t, EncodingKitty.Render(id, 2, 2))
	EncodingKitty.Forget(id, 2, 2, false)
}

//nolint:paralleltest // Clears the package-level cache.
func TestResetIdempotent(t *testing.T) {
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

func TestKittyTransmitConcurrentlyForOneKeySendsItOnce(t *testing.T) {
	t.Parallel()

	const id = "kitty-transmit-concurrently"
	img := image.NewRGBA(image.Rect(0, 0, 20, 40))
	cs := CellSize{Width: 10, Height: 20}
	cmds := make([]tea.Cmd, 16)
	for i := range cmds {
		cmds[i] = EncodingKitty.Transmit(id, img, cs, 2, 2, false)
	}
	results := make(chan tea.Msg, len(cmds))
	var wg sync.WaitGroup
	for _, cmd := range cmds {
		wg.Go(func() { results <- cmd() })
	}
	wg.Wait()
	close(results)
	sent := 0
	for msg := range results {
		if _, ok := msg.(tea.RawMsg); ok {
			sent++
		}
	}
	require.Equal(t, 1, sent)
}

func TestResizeToCells(t *testing.T) {
	t.Parallel()

	img := image.NewRGBA(image.Rect(0, 0, 100, 200))
	tests := []struct {
		name string
		cs   CellSize
		want image.Rectangle
	}{
		{"unknown width keeps it", CellSize{Width: 0, Height: 20}, img.Bounds()},
		{"unknown height keeps it", CellSize{Width: 10, Height: 0}, img.Bounds()},
		{"fits the taller limit", CellSize{Width: 10, Height: 20}, image.Rect(0, 0, 30, 60)},
		{"fits the wider limit", CellSize{Width: 5, Height: 40}, image.Rect(0, 0, 20, 40)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, resizeToCells(img, tt.cs, 4, 3).Bounds())
		})
	}
}

func TestForgetDropsTheImage(t *testing.T) {
	t.Parallel()

	const id = "forget-drops"
	img := image.NewRGBA(image.Rect(0, 0, 20, 40))
	EncodingKitty.Transmit(id, img, CellSize{Width: 10, Height: 20}, 2, 2, false)()
	require.True(t, HasTransmitted(id, 2, 2))

	EncodingKitty.Forget(id, 2, 2, false)
	require.False(t, HasTransmitted(id, 2, 2))
}

func TestForgetDeletesKittyImagesInTheTerminal(t *testing.T) {
	t.Parallel()

	const id = "forget-kitty"
	key := imageKey{id: id, cols: 2, rows: 2}
	want := ansi.KittyGraphics(nil, "a=d", "d=I", fmt.Sprintf("i=%d", key.Hash()), "q=2")

	cmd := EncodingKitty.Forget(id, 2, 2, false)
	require.NotNil(t, cmd)
	require.Equal(t, tea.RawMsg{Msg: want}, cmd())

	cmd = EncodingKitty.Forget(id, 2, 2, true)
	require.NotNil(t, cmd)
	require.Equal(t, tea.RawMsg{Msg: ansi.TmuxPassthrough(want)}, cmd())
}

func TestForgetBlocksImageSendsNothing(t *testing.T) {
	t.Parallel()

	const id = "forget-blocks"
	img := image.NewRGBA(image.Rect(0, 0, 20, 40))
	EncodingBlocks.Transmit(id, img, CellSize{Width: 10, Height: 20}, 2, 2, false)()
	require.True(t, HasTransmitted(id, 2, 2))

	require.Nil(t, EncodingBlocks.Forget(id, 2, 2, false), "the terminal holds nothing to delete")
	require.False(t, HasTransmitted(id, 2, 2))
}

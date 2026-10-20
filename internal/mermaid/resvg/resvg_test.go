package resvg

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const rectSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10"><rect width="20" height="10" fill="#ff0000"/></svg>`

func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func TestRender(t *testing.T) {
	t.Parallel()

	e, err := New(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	img, err := e.Render([]byte(rectSVG), 40, 0)
	require.NoError(t, err)
	require.Equal(t, 40, img.Bounds().Dx())
	require.Equal(t, 20, img.Bounds().Dy(), "height follows the aspect ratio")
	r, g, b, a := img.At(5, 5).RGBA()
	require.Equal(t, [4]uint32{0xffff, 0, 0, 0xffff}, [4]uint32{r, g, b, a})

	_, err = e.Render([]byte("not svg"), 10, 10)
	require.ErrorIs(t, err, ErrInvalidSVG)
	_, err = e.Render([]byte(rectSVG), 10, 10)
	require.NoError(t, err, "the engine recovers after an error")
}

// Renders across GCs reuse the Engine's one instance, so the heap stays
// flat.
//
//nolint:paralleltest // Measures the process heap.
func TestRenderAcrossGCsDoesNotGrow(t *testing.T) {
	e, err := New(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	_, err = e.Render([]byte(rectSVG), 400, 200)
	require.NoError(t, err)
	before := heapAlloc()
	for range 20 {
		_, err = e.Render([]byte(rectSVG), 400, 200)
		require.NoError(t, err)
		runtime.GC()
	}
	after := heapAlloc()
	growth := int64(after) - int64(before)
	require.Less(t, growth, int64(2<<20), fmt.Sprintf("heap grew %d bytes over 20 GCs", growth))
}

func TestRenderRecyclesGrownInstance(t *testing.T) {
	t.Parallel()

	e, err := New(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	e.maxMemory = 8 << 20

	_, err = e.Render([]byte(rectSVG), 10, 10)
	require.NoError(t, err)
	require.NotZero(t, e.MemorySize(), "a small render keeps its instance")

	_, err = e.Render([]byte(rectSVG), 2000, 2000)
	require.NoError(t, err)
	require.Zero(t, e.MemorySize(), "memory grown past the limit is dropped")

	_, err = e.Render([]byte(rectSVG), 10, 10)
	require.NoError(t, err)
	require.NotZero(t, e.MemorySize())
	require.Less(t, e.MemorySize(), uint32(8<<20), "the fresh instance starts small")
}

func TestRenderPastTheMemoryLimitFailsAndRecovers(t *testing.T) {
	t.Parallel()

	e, err := NewWithLimits(t.Context(), Limits{Memory: 16 << 20, Timeout: DefaultLimits.Timeout})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	_, err = e.Render([]byte(rectSVG), 4000, 2000) // 32 MB of pixels.
	require.ErrorContains(t, err, "wasm error", "the module traps when it can't grow its memory")
	_, err = e.Render([]byte(rectSVG), 40, 20)
	require.NoError(t, err, "the next render starts from a fresh instance")
}

func TestRenderPastItsDeadlineFailsAndRecovers(t *testing.T) {
	t.Parallel()

	e, err := NewWithLimits(t.Context(), Limits{Memory: DefaultLimits.Memory, Timeout: time.Nanosecond})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	_, err = e.Render([]byte(rectSVG), 40, 20)
	require.ErrorIs(t, err, ErrRenderTimeout)
	e.limits.Timeout = DefaultLimits.Timeout
	_, err = e.Render([]byte(rectSVG), 40, 20)
	require.NoError(t, err, "the next render starts from a fresh instance")
}

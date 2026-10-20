package resvg

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

// A render that grows the instance's memory past maxMemory replaces the
// instance, so the grown memory is released instead of held for good.
//
//nolint:paralleltest // Measures the process heap.
func TestRenderReleasesGrownMemory(t *testing.T) {
	e, err := New(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	e.maxMemory = 8 << 20

	_, err = e.Render([]byte(rectSVG), 10, 10)
	require.NoError(t, err)
	before := heapAlloc()
	_, err = e.Render([]byte(rectSVG), 2000, 2000) // 16 MB of pixels.
	require.NoError(t, err)
	after := heapAlloc()
	growth := int64(after) - int64(before)
	require.Less(t, growth, int64(8<<20), fmt.Sprintf("heap grew %d bytes", growth))

	_, err = e.Render([]byte(rectSVG), 10, 10)
	require.NoError(t, err, "the next render starts from a fresh instance")
}

func TestRenderPastTheMemoryLimitFailsAndRecovers(t *testing.T) {
	t.Parallel()

	e, err := newWithLimits(t.Context(), renderLimits{Memory: 16 << 20, Timeout: defaultLimits.Timeout})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	_, err = e.Render([]byte(rectSVG), 4000, 2000) // 32 MB of pixels.
	require.ErrorContains(t, err, "wasm error", "the module traps when it can't grow its memory")
	_, err = e.Render([]byte(rectSVG), 40, 20)
	require.NoError(t, err, "the next render starts from a fresh instance")
}

func TestRenderPastItsDeadlineFailsAndRecovers(t *testing.T) {
	t.Parallel()

	// A deadline already past, so the render can't finish first however
	// coarse the platform's timers are.
	e, err := newWithLimits(t.Context(), renderLimits{Memory: defaultLimits.Memory, Timeout: -time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	_, err = e.Render([]byte(rectSVG), 40, 20)
	require.ErrorIs(t, err, ErrRenderTimeout)
	e.limits.Timeout = defaultLimits.Timeout
	_, err = e.Render([]byte(rectSVG), 40, 20)
	require.NoError(t, err, "the next render starts from a fresh instance")
}

func TestNewIsSafeToCallConcurrently(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			e, err := New(t.Context())
			if assert.NoError(t, err) {
				_ = e.Close()
			}
		})
	}
	wg.Wait()
}

func TestDefaultLimits(t *testing.T) {
	t.Parallel()

	require.Equal(t, renderLimits{Memory: 256 << 20, Timeout: 10 * time.Second}, defaultLimits)
	require.Equal(t, 65536, wasmPageSize)
	require.Equal(t, 32<<20, defaultMaxMemory)
}

// An instance is replaced only once its memory grows past maxMemory, so
// one sitting exactly at it is kept.
func TestRenderKeepsAnInstanceAtMaxMemory(t *testing.T) {
	t.Parallel()

	e, err := New(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })

	_, err = e.Render([]byte(rectSVG), 10, 10)
	require.NoError(t, err)
	inst := e.inst
	e.maxMemory = inst.mod.Memory().Size()
	_, err = e.Render([]byte(rectSVG), 10, 10)
	require.NoError(t, err)
	require.Same(t, inst, e.inst, "an instance at maxMemory is kept")

	e.maxMemory--
	_, err = e.Render([]byte(rectSVG), 10, 10)
	require.NoError(t, err)
	require.Nil(t, e.inst, "an instance past maxMemory is dropped")
}

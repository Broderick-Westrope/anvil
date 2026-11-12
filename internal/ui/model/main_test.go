package model

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/pele-go/raster"
)

// testCompileCacheDir is the resvg compile cache every render test in this
// binary shares. A cold compile takes seconds under the race detector, and
// tests that each compiled into their own cache starved the package of CPU
// until workers missed their deadlines.
var testCompileCacheDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "anvil-ui-model-wasm-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create the shared compile cache:", err)
		os.Exit(1)
	}
	testCompileCacheDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// warmCompileCache compiles resvg into testCompileCacheDir once, so render
// tests load it in milliseconds and the one compile happens before any test
// starts waiting on a worker.
var warmCompileCache = sync.OnceValue(func() error {
	r, err := raster.NewRasterizer(context.Background(), testCompileCacheDir)
	if err != nil {
		return err
	}
	return r.Close()
})

// Package app: runtime memory observability.
//
// The 2026-09-16 incident (RSS >24GB during concurrent subagent
// streams) was not diagnosable from logs because nothing recorded
// heap growth or event-broker saturation. This monitor samples
// runtime.MemStats and the fan-in broker's drop counters, logging
// when memory grows notably, stays above the alert floor, or events
// are being dropped.
package app

import (
	"context"
	"log/slog"
	"runtime"
	"time"
)

const (
	// memSampleInterval is how often the monitor samples MemStats.
	// ReadMemStats briefly stops the world, so keep this coarse.
	memSampleInterval = 15 * time.Second

	// memGrowthLogDelta is the heap-alloc growth between samples that
	// triggers a log line even below the alert floor.
	memGrowthLogDelta = 256 << 20 // 256 MiB

	// memAlertFloor is the heap-alloc level above which every sample
	// is logged, so a runaway process leaves a growth curve in the
	// log file.
	memAlertFloor = 1 << 30 // 1 GiB
)

// startMemoryMonitor launches a goroutine that periodically samples
// memory and event-broker health, logging only when something is
// noteworthy. It exits when ctx is cancelled.
func (app *App) startMemoryMonitor(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(memSampleInterval)
		defer ticker.Stop()

		var lastLoggedHeap uint64
		var lastDrops, lastMustDrops uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				drops := app.events.DropCount()
				mustDrops := app.events.MustDeliverDropCount()

				grew := ms.HeapAlloc >= lastLoggedHeap+memGrowthLogDelta ||
					(ms.HeapAlloc+memGrowthLogDelta <= lastLoggedHeap)
				high := ms.HeapAlloc >= memAlertFloor
				dropped := drops != lastDrops || mustDrops != lastMustDrops
				if !grew && !high && !dropped {
					continue
				}

				slog.Info("Memory status",
					"heap_alloc_mb", ms.HeapAlloc>>20,
					"heap_sys_mb", ms.HeapSys>>20,
					"heap_released_mb", ms.HeapReleased>>20,
					"total_alloc_mb", ms.TotalAlloc>>20,
					"num_gc", ms.NumGC,
					"gc_cpu_pct", int(ms.GCCPUFraction*100),
					"goroutines", runtime.NumGoroutine(),
					"event_drops", drops,
					"event_must_deliver_drops", mustDrops,
				)
				lastLoggedHeap = ms.HeapAlloc
				lastDrops = drops
				lastMustDrops = mustDrops
			}
		}
	}()
}

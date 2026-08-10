package chat

import (
	"context"
	"os"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// BenchmarkResizeSession reproduces the resize re-render path over a real
// session's messages. Point ANVIL_BENCH_SESSION at a full session id and
// ANVIL_BENCH_DATADIR at the anvil data dir (defaults to ./.anvil).
//
//	ANVIL_BENCH_SESSION=e6368d820207a406 go test ./internal/ui/chat/ \
//	  -run x -bench BenchmarkResizeSession -benchtime 20x -cpuprofile /tmp/cpu.out
func BenchmarkResizeSession(b *testing.B) {
	sessionID := os.Getenv("ANVIL_BENCH_SESSION")
	if sessionID == "" {
		b.Skip("set ANVIL_BENCH_SESSION to a full session id")
	}
	dataDir := os.Getenv("ANVIL_BENCH_DATADIR")
	if dataDir == "" {
		dataDir = ".anvil"
	}

	ctx := context.Background()
	conn, err := db.Connect(ctx, dataDir)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	// Note: intentionally not closing conn. db.Connect pools connections by
	// path, and the testing framework may invoke this function more than
	// once; closing would break the shared pooled *sql.DB on re-entry.

	svc := message.NewService(db.New(conn))
	msgs, err := svc.List(ctx, sessionID)
	if err != nil {
		b.Fatalf("list messages: %v", err)
	}
	if len(msgs) == 0 {
		b.Fatalf("no messages for session %s", sessionID)
	}
	b.Logf("loaded %d messages", len(msgs))

	ptrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		ptrs[i] = &msgs[i]
	}
	toolResults := BuildToolResultMap(ptrs)

	sty := styles.TokyoNight()
	var items []list.Item
	for _, m := range ptrs {
		for _, it := range ExtractMessageItems(&sty, m, toolResults, nil) {
			items = append(items, it)
		}
	}
	b.Logf("built %d items", len(items))

	l := list.NewList(items...)

	b.ResetTimer()
	// Alternate widths so every iteration is a genuine width change, which
	// is what invalidates the caches and forces a full re-render — exactly
	// what a resize drag does.
	widths := []int{100, 99}
	i := 0
	for b.Loop() {
		l.SetSize(widths[i%2], 40)
		_ = l.TotalHeight()
		i++
	}
}

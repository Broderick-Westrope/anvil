package mcp

import (
	"context"
	"time"
)

// settlePoll bounds how long WaitSettled can miss a transition: the
// event broker drops events for slow subscribers, so it also re-reads the
// state on a timer.
const settlePoll = 200 * time.Millisecond

// WaitSettled blocks until the named server leaves StateStarting, and
// returns its state then. A server that isn't starting returns at once.
// When ctx ends first it returns the last state seen and ctx's error.
func WaitSettled(ctx context.Context, name string) (ClientInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := SubscribeEvents(ctx)
	ticker := time.NewTicker(settlePoll)
	defer ticker.Stop()
	for {
		// Read after subscribing, so a transition between the two
		// can't be missed.
		info, _ := GetState(name)
		if info.State != StateStarting {
			return info, nil
		}
		select {
		case <-ctx.Done():
			return info, ctx.Err()
		case <-events:
		case <-ticker.C:
		}
	}
}

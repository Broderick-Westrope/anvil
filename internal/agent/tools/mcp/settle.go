package mcp

import (
	"context"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/csync"
)

// connecting holds the servers a ConnectDeferred call is connecting,
// across its retry.
var connecting = csync.NewMap[string, struct{}]()

// settlePoll bounds how long WaitSettled can miss a transition: the
// event broker drops events for slow subscribers, so it also re-reads the
// state on a timer.
const settlePoll = 200 * time.Millisecond

// WaitSettled blocks until the named server leaves StateStarting and no
// ConnectDeferred call is still connecting it, and returns its state
// then. A server that is neither returns at once.
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
		if _, busy := connecting.Get(name); info.State != StateStarting && !busy {
			return info, nil
		}
		select {
		case <-ctx.Done():
			return info, ctx.Err()
		case _, open := <-events:
			if !open {
				// A shut-down broker closes the channel; fall back
				// to the timer rather than spin.
				events = nil
			}
		case <-ticker.C:
		}
	}
}

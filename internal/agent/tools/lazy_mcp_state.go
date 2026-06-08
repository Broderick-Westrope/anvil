package tools

import (
	"context"
	"maps"
	"sync"
)

// lazyMCPStateKey is the context key for LazyMCPState.
type lazyMCPStateKey struct{}

// LazyMCPState tracks which lazy MCP servers have been enabled in the
// current conversation branch.
type LazyMCPState struct {
	mu      sync.Mutex
	enabled map[string]bool
}

// NewLazyMCPState creates a new LazyMCPState with the given initial set
// of enabled servers. If initial is nil an empty map is used.
func NewLazyMCPState(initial map[string]bool) *LazyMCPState {
	m := make(map[string]bool)
	if initial != nil {
		maps.Copy(m, initial)
	}
	return &LazyMCPState{enabled: m}
}

// Enable marks the named server as enabled. It returns true if the
// server was already enabled.
func (s *LazyMCPState) Enable(name string) (alreadyEnabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled[name] {
		return true
	}
	s.enabled[name] = true
	return false
}

// IsEnabled reports whether the named server has been enabled.
func (s *LazyMCPState) IsEnabled(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled[name]
}

// EnabledSet returns a copy of the enabled map.
func (s *LazyMCPState) EnabledSet() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.enabled)
}

// WithLazyMCPState returns a new context carrying the given
// LazyMCPState.
func WithLazyMCPState(ctx context.Context, state *LazyMCPState) context.Context {
	return context.WithValue(ctx, lazyMCPStateKey{}, state)
}

// GetLazyMCPState retrieves the LazyMCPState from the context. It
// returns nil if no state is present.
func GetLazyMCPState(ctx context.Context) *LazyMCPState {
	v, _ := ctx.Value(lazyMCPStateKey{}).(*LazyMCPState)
	return v
}

package workspace

import (
	"context"
	"sync"

	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// maxSessionDepth bounds the parent walk so a corrupt parent chain can't
// loop forever.
const maxSessionDepth = 16

// sessionAncestry resolves whether a job's owning session descends from a
// given session. Parent links never change once a session exists, so
// resolved chains are cached for the life of the process.
type sessionAncestry struct {
	parentOf func(ctx context.Context, sessionID string) (string, error)

	mu      sync.Mutex
	parents map[string]string
}

func newSessionAncestry(parentOf func(ctx context.Context, sessionID string) (string, error)) *sessionAncestry {
	return &sessionAncestry{parentOf: parentOf, parents: make(map[string]string)}
}

// parent returns sessionID's parent, or "" for a root session. Lookup
// failures are not cached so a session that isn't saved yet is retried.
func (a *sessionAncestry) parent(ctx context.Context, sessionID string) (string, bool) {
	a.mu.Lock()
	parentID, ok := a.parents[sessionID]
	a.mu.Unlock()
	if ok {
		return parentID, true
	}

	parentID, err := a.parentOf(ctx, sessionID)
	if err != nil {
		return "", false
	}
	a.mu.Lock()
	a.parents[sessionID] = parentID
	a.mu.Unlock()
	return parentID, true
}

// descendsFrom reports whether sessionID is rootID or one of its
// descendants.
func (a *sessionAncestry) descendsFrom(ctx context.Context, sessionID, rootID string) bool {
	current := sessionID
	for range maxSessionDepth {
		if current == rootID {
			return true
		}
		if current == "" {
			return false
		}
		parentID, ok := a.parent(ctx, current)
		if !ok {
			return false
		}
		current = parentID
	}
	return false
}

// filterSessionTreeJobs keeps the jobs owned by rootID or a descendant
// session, preserving the input order.
func (a *sessionAncestry) filterSessionTreeJobs(ctx context.Context, jobs []shell.JobInfo, rootID string) []shell.JobInfo {
	var out []shell.JobInfo
	for _, job := range jobs {
		if a.descendsFrom(ctx, job.SessionID, rootID) {
			out = append(out, job)
		}
	}
	return out
}

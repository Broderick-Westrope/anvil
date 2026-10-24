package mcp

import (
	"context"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
)

// This file contains test-only helpers exported for cross-package tests.
// They MUST NOT be used in production code. The ForTest suffix signals
// this intent; the helpers are kept in a non-_test.go file because
// consumers live in sibling packages (e.g. internal/agent/tools).

// SetStateForTest sets a server's state in the global registry.
// Intended exclusively for tests that need to seed specific states
// without calling Initialize. Pair with t.Cleanup(DeleteStateForTest).
func SetStateForTest(name string, state State) {
	updateState(name, state, nil, nil, Counts{})
}

// SetStateWithErrorForTest is like SetStateForTest but also seeds an
// error. This allows tests to exercise code paths that branch on
// NeedsAuth without calling Initialize.
func SetStateWithErrorForTest(name string, state State, err error) {
	updateState(name, state, err, nil, Counts{})
}

// DeleteStateForTest removes a server's state from the global registry.
// Use in t.Cleanup after SetStateForTest.
func DeleteStateForTest(name string) {
	states.Del(name)
}

// SetToolsForTest registers tools for a server as a connect would, without
// connecting anything. Pair with t.Cleanup(DeleteToolsForTest).
func SetToolsForTest(name string, tools ...*Tool) {
	var registered []*RegisteredTool
	for _, tool := range tools {
		registered = append(registered, newRegisteredTool(name, tool))
	}
	allTools.Set(name, registered)
}

// DeleteToolsForTest removes a server's tools from the registry.
func DeleteToolsForTest(name string) {
	allTools.Del(name)
}

// SetConnectingForTest marks a server as being connected by a
// ConnectDeferred call, as during its retry, or clears the mark.
func SetConnectingForTest(name string, busy bool) {
	if busy {
		connecting.Set(name, struct{}{})
		return
	}
	clearConnecting(name)
}

// FailConnectsForTest makes every new client session fail with err, and
// returns a function that restores real connects. Pair with t.Cleanup. It
// swaps a package-level function, so callers must not run in parallel.
func FailConnectsForTest(err error) (restore func()) {
	orig := newSession
	newSession = func(context.Context, string, config.MCPConfig, config.VariableResolver, db.Querier) (*ClientSession, error) {
		return nil, err
	}
	return func() { newSession = orig }
}

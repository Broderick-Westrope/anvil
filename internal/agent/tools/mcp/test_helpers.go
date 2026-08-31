package mcp

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

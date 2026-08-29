package mcp

// SetStateForTest sets a server's state in the global registry.
// Intended exclusively for tests that need to seed specific states
// without calling Initialize.
func SetStateForTest(name string, state State) {
	updateState(name, state, nil, nil, Counts{})
}

// DeleteStateForTest removes a server's state from the global registry.
// Use in t.Cleanup after SetStateForTest.
func DeleteStateForTest(name string) {
	states.Del(name)
}

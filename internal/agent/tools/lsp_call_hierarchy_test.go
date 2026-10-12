package tools

import (
	"encoding/json"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
)

// fakeCallHierarchyItem is a call hierarchy item named name at line (0-based)
// of path.
func fakeCallHierarchyItem(name, path string, line uint32) protocol.CallHierarchyItem {
	r := protocol.Range{Start: protocol.Position{Line: line}, End: protocol.Position{Line: line, Character: 3}}
	return protocol.CallHierarchyItem{
		Name:           name,
		Kind:           protocol.Function,
		URI:            protocol.DocumentURI(protocol.URIFromPath(path)),
		Range:          r,
		SelectionRange: r,
	}
}

func runCallHierarchyTool(t *testing.T, tool fantasy.AgentTool, params CallHierarchyParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "test-call", Name: CallHierarchyToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestCallHierarchyTool_RejectsInvalidParams(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		params CallHierarchyParams
		want   string
	}{
		"missing symbol":    {params: CallHierarchyParams{Direction: "incoming"}, want: "symbol is required"},
		"missing direction": {params: CallHierarchyParams{Symbol: "Foo"}, want: "direction must be 'incoming' or 'outgoing'"},
		"unknown direction": {params: CallHierarchyParams{Symbol: "Foo", Direction: "sideways"}, want: "direction must be 'incoming' or 'outgoing'"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resp := runCallHierarchyTool(t, NewCallHierarchyTool(nil), tc.params)

			require.True(t, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

func TestCallHierarchyTool_SymbolNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFakeLangFile(t, dir, "a.fakelang", "func Other() {}\n")
	manager := newFakeLSP(t, dir, nil)

	resp := runCallHierarchyTool(t, NewCallHierarchyTool(manager), CallHierarchyParams{Symbol: "Foo", Direction: "incoming", Path: dir})

	require.False(t, resp.IsError)
	require.Equal(t, "Symbol 'Foo' not found", resp.Content)
}

func TestCallHierarchyTool_Prepare(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		reply     fakeLSPReply
		want      string
		wantError bool
	}{
		"prepare fails": {
			reply:     fakeLSPReply{Error: "boom"},
			want:      "failed to prepare call hierarchy: prepare call hierarchy request failed: jsonrpc2: code -32603 message: boom",
			wantError: true,
		},
		"nothing to show": {
			reply: fakeLSPReply{Result: []protocol.CallHierarchyItem{}},
			want:  "No call hierarchy information for 'Foo'",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFakeLangFile(t, dir, "a.fakelang", "func Foo() {}\n")
			manager := newFakeLSP(t, dir, map[string]fakeLSPReply{"textDocument/prepareCallHierarchy": tc.reply})

			resp := runCallHierarchyTool(t, NewCallHierarchyTool(manager), CallHierarchyParams{Symbol: "Foo", Direction: "incoming", Path: dir})

			require.Equal(t, tc.wantError, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

func TestCallHierarchyTool_Calls(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeFakeLangFile(t, dir, "a.fakelang", "func Foo() {}\n")
	callerPath := writeFakeLangFile(t, dir, "b.fakelang", "x\nx\n")
	target := fakeCallHierarchyItem("Foo", path, 0)
	shownPath, shownCallerPath := lspShownPath(t, path), lspShownPath(t, callerPath)
	first := fakeCallHierarchyItem("First", callerPath, 0)
	second := fakeCallHierarchyItem("Second", path, 6)
	prepare := fakeLSPReply{Result: []protocol.CallHierarchyItem{target, first}}

	tests := map[string]struct {
		direction string
		replies   map[string]fakeLSPReply
		want      string
		wantError bool
	}{
		"incoming callers": {
			direction: "incoming",
			replies: map[string]fakeLSPReply{"callHierarchy/incomingCalls": {Result: []protocol.CallHierarchyIncomingCall{
				{From: first}, {From: second},
			}}},
			want: "Call hierarchy for 'Foo':\n\n2 caller(s):\n\n" +
				"  " + shownCallerPath + ":1 — First\n" +
				"  " + shownPath + ":7 — Second\n",
		},
		"no incoming calls": {
			direction: "incoming",
			replies:   map[string]fakeLSPReply{"callHierarchy/incomingCalls": {Result: []protocol.CallHierarchyIncomingCall{}}},
			want:      "Call hierarchy for 'Foo':\n\nNo incoming calls found.\n",
		},
		"incoming calls fail": {
			direction: "incoming",
			replies:   map[string]fakeLSPReply{"callHierarchy/incomingCalls": {Error: "boom"}},
			want:      "failed to get incoming calls: incoming calls request failed: jsonrpc2: code -32603 message: boom",
			wantError: true,
		},
		"outgoing callees": {
			direction: "outgoing",
			replies: map[string]fakeLSPReply{"callHierarchy/outgoingCalls": {Result: []protocol.CallHierarchyOutgoingCall{
				{To: second}, {To: first},
			}}},
			want: "Call hierarchy for 'Foo':\n\n2 callee(s):\n\n" +
				"  " + shownPath + ":7 — Second\n" +
				"  " + shownCallerPath + ":1 — First\n",
		},
		"no outgoing calls": {
			direction: "outgoing",
			replies:   map[string]fakeLSPReply{"callHierarchy/outgoingCalls": {Result: []protocol.CallHierarchyOutgoingCall{}}},
			want:      "Call hierarchy for 'Foo':\n\nNo outgoing calls found.\n",
		},
		"outgoing calls fail": {
			direction: "outgoing",
			replies:   map[string]fakeLSPReply{"callHierarchy/outgoingCalls": {Error: "boom"}},
			want:      "failed to get outgoing calls: outgoing calls request failed: jsonrpc2: code -32603 message: boom",
			wantError: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tc.replies["textDocument/prepareCallHierarchy"] = prepare
			manager := newFakeLSP(t, dir, tc.replies)

			resp := runCallHierarchyTool(t, NewCallHierarchyTool(manager), CallHierarchyParams{Symbol: "Foo", Direction: tc.direction, Path: dir})

			require.Equal(t, tc.wantError, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

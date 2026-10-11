package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
)

func TestFormatRenameCandidates(t *testing.T) {
	t.Parallel()

	candidates := []*resolvedSymbol{
		{path: "/proj/a.go", line: 10},
		{path: "/proj/b.go", line: 42},
	}

	got := formatRenameCandidates("Foo", candidates)

	require.Equal(t, `Symbol 'Foo' matches in multiple files:

  /proj/a.go:10
  /proj/b.go:42

Retry with file_path set to the file containing the symbol you want to rename.`, got)
}

func TestFormatRenameResult(t *testing.T) {
	t.Parallel()

	counts := map[string]int{
		"/proj/b.go": 1,
		"/proj/a.go": 3,
	}

	got := formatRenameResult("Foo", "Bar", counts)

	require.Equal(t, `Renamed 'Foo' to 'Bar' in 2 file(s):

  /proj/a.go: 3 renames
  /proj/b.go: 1 rename
`, got)
}

func TestCountEditsByFile(t *testing.T) {
	t.Parallel()

	aURI := protocol.URIFromPath("/proj/a.go")
	bURI := protocol.URIFromPath("/proj/b.go")

	edit := &protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentURI][]protocol.TextEdit{
			aURI: {{NewText: "Bar"}, {NewText: "Bar"}},
		},
		DocumentChanges: []protocol.DocumentChange{
			{
				TextDocumentEdit: &protocol.TextDocumentEdit{
					TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{
						TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: bURI},
					},
					Edits: make([]protocol.Or_TextDocumentEdit_edits_Elem, 3),
				},
			},
		},
	}

	counts := countEditsByFile(edit)

	aPath, err := aURI.Path()
	require.NoError(t, err)
	bPath, err := bURI.Path()
	require.NoError(t, err)

	require.Equal(t, map[string]int{
		aPath: 2,
		bPath: 3,
	}, counts)
}

func TestFindSymbolByName(t *testing.T) {
	t.Parallel()

	method := protocol.DocumentSymbol{
		Name:           "DoWork",
		Kind:           protocol.Method,
		Range:          protocol.Range{Start: protocol.Position{Line: 12}},
		SelectionRange: protocol.Range{Start: protocol.Position{Line: 12, Character: 18}},
	}
	symbols := []protocol.DocumentSymbolResult{
		&protocol.DocumentSymbol{
			Name:     "Worker",
			Kind:     protocol.Struct,
			Children: []protocol.DocumentSymbol{method},
		},
		&protocol.DocumentSymbol{Name: "main", Kind: protocol.Function},
	}

	t.Run("finds top-level symbol", func(t *testing.T) {
		t.Parallel()
		got := findSymbolByName(symbols, "main")
		require.NotNil(t, got)
		require.Equal(t, "main", got.GetName())
	})

	t.Run("finds nested symbol", func(t *testing.T) {
		t.Parallel()
		got := findSymbolByName(symbols, "DoWork")
		require.NotNil(t, got)
		require.Equal(t, "DoWork", got.GetName())
	})

	t.Run("returns nil when missing", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, findSymbolByName(symbols, "nope"))
	})
}

func TestSymbolSelectionStart(t *testing.T) {
	t.Parallel()

	t.Run("uses selection range for DocumentSymbol", func(t *testing.T) {
		t.Parallel()
		sym := &protocol.DocumentSymbol{
			Name:           "Foo",
			Range:          protocol.Range{Start: protocol.Position{Line: 3}},
			SelectionRange: protocol.Range{Start: protocol.Position{Line: 5, Character: 6}},
		}
		got := symbolSelectionStart(sym)
		require.Equal(t, protocol.Position{Line: 5, Character: 6}, got)
	})

	t.Run("falls back to range start for SymbolInformation", func(t *testing.T) {
		t.Parallel()
		sym := &protocol.SymbolInformation{
			Name: "Foo",
			Location: protocol.Location{
				Range: protocol.Range{Start: protocol.Position{Line: 7, Character: 2}},
			},
		}
		got := symbolSelectionStart(sym)
		require.Equal(t, protocol.Position{Line: 7, Character: 2}, got)
	})
}

const renameTestSource = "func Foo() {}\nFoo()\n"

func runRenameTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params RenameParams) (fantasy.ToolResponse, error) {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	return tool.Run(ctx, fantasy.ToolCall{ID: "test-call", Name: RenameToolName, Input: string(input)})
}

// fooToBarEdit renames both uses of Foo in renameTestSource at path.
func fooToBarEdit(path string) protocol.WorkspaceEdit {
	at := func(line, char uint32) protocol.Range {
		return protocol.Range{Start: protocol.Position{Line: line, Character: char}, End: protocol.Position{Line: line, Character: char + 3}}
	}
	return protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{
		protocol.DocumentURI(protocol.URIFromPath(path)): {
			{Range: at(0, 5), NewText: "Bar"},
			{Range: at(1, 0), NewText: "Bar"},
		},
	}}
}

// fooDocumentSymbols are the document symbols of renameTestSource.
var fooDocumentSymbols = []protocol.DocumentSymbol{{
	Name:           "Foo",
	Kind:           protocol.Function,
	Range:          protocol.Range{End: protocol.Position{Character: 13}},
	SelectionRange: protocol.Range{Start: protocol.Position{Character: 5}, End: protocol.Position{Character: 8}},
}}

func renamedFooResult(t *testing.T, path string) string {
	t.Helper()
	return "Renamed 'Foo' to 'Bar' in 1 file(s):\n\n  " + lspShownPath(t, path) + ": 2 renames\n\n"
}

func TestRenameTool_RejectsInvalidParams(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		params RenameParams
		want   string
	}{
		"missing symbol":   {params: RenameParams{NewName: "Bar"}, want: "symbol is required"},
		"missing new name": {params: RenameParams{Symbol: "Foo"}, want: "new_name is required"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			resp, err := runRenameTool(t, NewRenameTool(nil, nil, nil, t.TempDir()), sessionCtx(), tc.params)

			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

func TestRenameTool_RenamesSymbolFoundInWorkspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeFakeLangFile(t, dir, "a.fakelang", renameTestSource)
	manager := newFakeLSP(t, dir, map[string]fakeLSPReply{"textDocument/rename": {Result: fooToBarEdit(path)}})
	perms := &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest](), allow: true}
	tracker := newMockFileTracker()

	resp, err := runRenameTool(t, NewRenameTool(manager, perms, tracker, dir), sessionCtx(), RenameParams{Symbol: "Foo", NewName: "Bar"})

	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, renamedFooResult(t, path), resp.Content)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "func Bar() {}\nBar()\n", string(got))
	require.Equal(t, "test-session", perms.lastRequest.SessionID)
	require.Equal(t, RenameToolName, perms.lastRequest.ToolName)
	require.Equal(t, "Rename 'Foo' to 'Bar'", perms.lastRequest.Description)
	require.Equal(t, path, perms.lastRequest.Input)
	require.Equal(t, filetracker.HashContent(got), tracker.hashes[lspShownPath(t, path)])
}

func TestRenameTool_PermissionRequestIdentifiesCallAndPath(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		outsideWorkingDir bool
	}{
		"file inside working directory":  {},
		"file outside working directory": {outsideWorkingDir: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := writeFakeLangFile(t, dir, "a.fakelang", renameTestSource)
			manager := newFakeLSP(t, dir, map[string]fakeLSPReply{
				"textDocument/documentSymbol": {Result: fooDocumentSymbols},
				"textDocument/rename":         {Result: fooToBarEdit(path)},
			})
			perms := &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest](), allow: true}
			workingDir, wantPath := dir, dir
			if tc.outsideWorkingDir {
				workingDir, wantPath = t.TempDir(), path
			}

			resp, err := runRenameTool(t, NewRenameTool(manager, perms, nil, workingDir), sessionCtx(), RenameParams{Symbol: "Foo", NewName: "Bar", FilePath: path})

			require.NoError(t, err)
			require.False(t, resp.IsError, resp.Content)
			require.Equal(t, "test-call", perms.lastRequest.ToolCallID)
			require.Equal(t, wantPath, perms.lastRequest.Path)
			require.Equal(t, "write", perms.lastRequest.Action)
		})
	}
}

func TestRenameTool_WorkspaceResolution(t *testing.T) {
	t.Parallel()

	t.Run("symbol not found", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFakeLangFile(t, dir, "a.fakelang", "func Other() {}\n")
		manager := newFakeLSP(t, dir, nil)

		resp, err := runRenameTool(t, NewRenameTool(manager, nil, nil, dir), sessionCtx(), RenameParams{Symbol: "Foo", NewName: "Bar"})

		require.NoError(t, err)
		require.False(t, resp.IsError)
		require.Equal(t, "Symbol 'Foo' not found", resp.Content)
	})

	t.Run("symbol in several files", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		second := writeFakeLangFile(t, dir, "b.fakelang", "Foo()\n")
		first := writeFakeLangFile(t, dir, "a.fakelang", renameTestSource)
		manager := newFakeLSP(t, dir, nil)

		resp, err := runRenameTool(t, NewRenameTool(manager, nil, nil, dir), sessionCtx(), RenameParams{Symbol: "Foo", NewName: "Bar"})

		require.NoError(t, err)
		require.True(t, resp.IsError)
		require.Equal(t, "Symbol 'Foo' matches in multiple files:\n\n  "+lspShownPath(t, first)+":1\n  "+lspShownPath(t, second)+":1\n\n"+
			"Retry with file_path set to the file containing the symbol you want to rename.", resp.Content)
		got, err := os.ReadFile(first)
		require.NoError(t, err)
		require.Equal(t, renameTestSource, string(got))
	})
}

func TestRenameTool_FileResolution(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		file      string
		symbols   fakeLSPReply
		want      func(path string) string
		wantError bool
	}{
		"symbol in file": {
			file:    "a.fakelang",
			symbols: fakeLSPReply{Result: fooDocumentSymbols},
			want:    func(path string) string { return renamedFooResult(t, path) },
		},
		"file no client handles": {
			file:      "a.txt",
			symbols:   fakeLSPReply{Result: fooDocumentSymbols},
			want:      func(path string) string { return "no LSP client handles file: " + path },
			wantError: true,
		},
		"document symbols fail": {
			file:    "a.fakelang",
			symbols: fakeLSPReply{Error: "boom"},
			want: func(string) string {
				return "failed to get document symbols: document symbol request failed: jsonrpc2: code -32603 message: boom"
			},
			wantError: true,
		},
		"symbol not in file": {
			file:      "a.fakelang",
			symbols:   fakeLSPReply{Result: []protocol.DocumentSymbol{}},
			want:      func(path string) string { return "symbol 'Foo' not found in " + path },
			wantError: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := writeFakeLangFile(t, dir, tc.file, renameTestSource)
			// A second match elsewhere shows file_path skips workspace search.
			writeFakeLangFile(t, dir, "other.fakelang", "Foo()\n")
			manager := newFakeLSP(t, dir, map[string]fakeLSPReply{
				"textDocument/documentSymbol": tc.symbols,
				"textDocument/rename":         {Result: fooToBarEdit(path)},
			})

			resp, err := runRenameTool(t, NewRenameTool(manager, nil, nil, dir), sessionCtx(), RenameParams{Symbol: "Foo", NewName: "Bar", FilePath: path})

			require.NoError(t, err)
			require.Equal(t, tc.wantError, resp.IsError)
			require.Equal(t, tc.want(path), resp.Content)
		})
	}
}

func TestRenameTool_RenameFailures(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		reply fakeLSPReply
		want  string
	}{
		"not an identifier": {
			reply: fakeLSPReply{Error: "no identifier found"},
			want:  "The matched position for 'Foo' is not an identifier (it may be inside a comment or string). Retry with file_path set to the file containing the symbol.",
		},
		"server error": {
			reply: fakeLSPReply{Error: "boom"},
			want:  "rename failed: rename request failed: jsonrpc2: code -32603 message: boom",
		},
		"edit for a missing file": {
			reply: fakeLSPReply{Result: protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{
				protocol.DocumentURI(protocol.URIFromPath(filepath.Join(os.TempDir(), "anvil-missing", "gone.fakelang"))): {{NewText: "x"}},
			}}},
			want: "failed to apply rename edits: ",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFakeLangFile(t, dir, "a.fakelang", renameTestSource)
			manager := newFakeLSP(t, dir, map[string]fakeLSPReply{"textDocument/rename": tc.reply})
			perms := &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest](), allow: true}

			resp, err := runRenameTool(t, NewRenameTool(manager, perms, nil, dir), sessionCtx(), RenameParams{Symbol: "Foo", NewName: "Bar"})

			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.True(t, strings.HasPrefix(resp.Content, tc.want), resp.Content)
		})
	}
}

func TestRenameTool_EmptyEdit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFakeLangFile(t, dir, "a.fakelang", renameTestSource)
	manager := newFakeLSP(t, dir, map[string]fakeLSPReply{"textDocument/rename": {Result: protocol.WorkspaceEdit{}}})

	resp, err := runRenameTool(t, NewRenameTool(manager, nil, nil, dir), sessionCtx(), RenameParams{Symbol: "Foo", NewName: "Bar"})

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "Renamed 'Foo' to 'Bar' in 0 file(s):\n\n", resp.Content)
}

func TestRenameTool_Permission(t *testing.T) {
	t.Parallel()
	requestErr := errors.New("permission service down")
	tests := map[string]struct {
		ctx         context.Context
		permissions func() permission.Service
		wantAsked   bool
		wantRenamed bool
		want        string
		wantErr     error
	}{
		"granted": {
			ctx: sessionCtx(),
			permissions: func() permission.Service {
				return &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
			},
			wantAsked:   true,
			wantRenamed: true,
		},
		"denied": {
			ctx: sessionCtx(),
			permissions: func() permission.Service {
				return &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
			},
			wantAsked: true,
			want:      "Permission denied",
		},
		"request fails": {
			ctx: sessionCtx(),
			permissions: func() permission.Service {
				return &mockViewPermissionService{
					Broker: pubsub.NewBroker[permission.PermissionRequest](),
					requestFunc: func(context.Context, permission.CreatePermissionRequest) (permission.RequestResult, error) {
						return permission.RequestResult{}, requestErr
					},
				}
			},
			wantAsked: true,
			wantErr:   requestErr,
		},
		"no session skips the request": {
			ctx: context.Background(),
			permissions: func() permission.Service {
				return &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
			},
			wantRenamed: true,
		},
		"no permission service skips the request": {
			ctx:         sessionCtx(),
			permissions: func() permission.Service { return nil },
			wantRenamed: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := writeFakeLangFile(t, dir, "a.fakelang", renameTestSource)
			manager := newFakeLSP(t, dir, map[string]fakeLSPReply{"textDocument/rename": {Result: fooToBarEdit(path)}})
			perms := tc.permissions()
			tracker := newMockFileTracker()

			resp, err := runRenameTool(t, NewRenameTool(manager, perms, tracker, dir), tc.ctx, RenameParams{Symbol: "Foo", NewName: "Bar"})

			asked := 0
			switch p := perms.(type) {
			case *recordingPermissionService:
				asked = p.requestCount
			case *mockViewPermissionService:
				asked = p.requestCount()
			}
			require.Equal(t, tc.wantAsked, asked == 1)
			got, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.EqualError(t, err, "permission request failed: "+requestErr.Error())
				require.Equal(t, renameTestSource, string(got))
				return
			}
			require.NoError(t, err)
			if !tc.wantRenamed {
				require.True(t, resp.IsError)
				require.True(t, resp.StopTurn)
				require.Equal(t, tc.want, resp.Content)
				require.Equal(t, renameTestSource, string(got))
				return
			}
			require.Equal(t, renamedFooResult(t, path), resp.Content)
			require.Equal(t, "func Bar() {}\nBar()\n", string(got))
			_, recorded := tracker.hashes[lspShownPath(t, path)]
			require.Equal(t, tc.ctx != context.Background(), recorded)
		})
	}
}

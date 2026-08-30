package tools

import (
	"testing"

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

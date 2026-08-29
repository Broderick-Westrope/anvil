package tools

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
)

// resolvedSymbol holds the result of resolving a symbol name to an LSP position.
type resolvedSymbol struct {
	client *lsp.Client
	path   string
	line   int
	char   int
}

// resolveSymbol greps for a symbol name, triggers lazy LSP startup, and
// returns the first match position that a running client can serve.
func resolveSymbol(ctx context.Context, lspManager *lsp.Manager, symbol, workingDir string) (*resolvedSymbol, error) {
	matches, _, err := searchFiles(ctx, `\b`+regexp.QuoteMeta(symbol)+`\b`, workingDir, "", 100)
	if err != nil {
		return nil, fmt.Errorf("failed to search for symbol: %w", err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("symbol '%s' not found in grep results", symbol)
	}

	for _, match := range matches {
		absPath, err := filepath.Abs(match.path)
		if err != nil {
			continue
		}

		lspManager.Start(ctx, absPath)

		client := findLSPClient(lspManager, absPath)
		if client == nil {
			continue
		}

		return &resolvedSymbol{
			client: client,
			path:   absPath,
			line:   match.lineNum,
			char:   match.charNum + getSymbolOffset(symbol),
		}, nil
	}

	return nil, fmt.Errorf("no LSP client handles any file matching '%s'", symbol)
}

// resolveSymbolCandidates greps for a symbol name, triggers lazy LSP
// startup, and returns one candidate per file that a running client can
// serve, sorted by path.
func resolveSymbolCandidates(ctx context.Context, lspManager *lsp.Manager, symbol, workingDir string) ([]*resolvedSymbol, error) {
	matches, _, err := searchFiles(ctx, `\b`+regexp.QuoteMeta(symbol)+`\b`, workingDir, "", 100)
	if err != nil {
		return nil, fmt.Errorf("failed to search for symbol: %w", err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("symbol '%s' not found in grep results", symbol)
	}

	seen := make(map[string]struct{})
	var candidates []*resolvedSymbol
	for _, match := range matches {
		absPath, err := filepath.Abs(match.path)
		if err != nil {
			continue
		}
		if _, ok := seen[absPath]; ok {
			continue
		}
		seen[absPath] = struct{}{}

		lspManager.Start(ctx, absPath)

		client := findLSPClient(lspManager, absPath)
		if client == nil {
			continue
		}

		candidates = append(candidates, &resolvedSymbol{
			client: client,
			path:   absPath,
			line:   match.lineNum,
			char:   match.charNum + getSymbolOffset(symbol),
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no LSP client handles any file matching '%s'", symbol)
	}

	slices.SortFunc(candidates, func(a, b *resolvedSymbol) int {
		return strings.Compare(a.path, b.path)
	})
	return candidates, nil
}

// findSymbolByName searches for a symbol by name in the document symbol tree.
func findSymbolByName(symbols []protocol.DocumentSymbolResult, name string) protocol.DocumentSymbolResult {
	for _, sym := range symbols {
		if sym.GetName() == name {
			return sym
		}
		if ds, ok := sym.(*protocol.DocumentSymbol); ok && len(ds.Children) > 0 {
			children := make([]protocol.DocumentSymbolResult, len(ds.Children))
			for i := range ds.Children {
				children[i] = &ds.Children[i]
			}
			if found := findSymbolByName(children, name); found != nil {
				return found
			}
		}
	}
	return nil
}

// symbolSelectionStart returns the position to use for position-based LSP
// requests on a symbol: the selection range start (the symbol name itself)
// when available, falling back to the symbol range start.
func symbolSelectionStart(sym protocol.DocumentSymbolResult) protocol.Position {
	if ds, ok := sym.(*protocol.DocumentSymbol); ok {
		return ds.SelectionRange.Start
	}
	return sym.GetRange().Start
}

// findLSPClient returns the first LSP client that handles the given file path.
func findLSPClient(lspManager *lsp.Manager, filePath string) *lsp.Client {
	if abs, err := filepath.Abs(filePath); err == nil {
		filePath = abs
	}
	for c := range lspManager.Clients().Seq() {
		if c.HandlesFile(filePath) {
			lspManager.Touch(c.GetName())
			return c
		}
	}
	return nil
}

// collectAffectedFiles extracts all unique file paths from a WorkspaceEdit.
func collectAffectedFiles(edit *protocol.WorkspaceEdit) []string {
	seen := make(map[string]struct{})
	var files []string

	for uri := range edit.Changes {
		path, err := uri.Path()
		if err != nil {
			continue
		}
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}

	for _, change := range edit.DocumentChanges {
		if tde := change.TextDocumentEdit; tde != nil {
			path, err := tde.TextDocument.URI.Path()
			if err != nil {
				continue
			}
			if _, ok := seen[path]; !ok {
				seen[path] = struct{}{}
				files = append(files, path)
			}
		}
	}

	return files
}

// countEditsByFile counts the text edits per file in a WorkspaceEdit.
func countEditsByFile(edit *protocol.WorkspaceEdit) map[string]int {
	counts := make(map[string]int)

	for uri, textEdits := range edit.Changes {
		path, err := uri.Path()
		if err != nil {
			continue
		}
		counts[path] += len(textEdits)
	}

	for _, change := range edit.DocumentChanges {
		if tde := change.TextDocumentEdit; tde != nil {
			path, err := tde.TextDocument.URI.Path()
			if err != nil {
				continue
			}
			counts[path] += len(tde.Edits)
		}
	}

	return counts
}

// isNoIdentifierError checks if an error indicates the grep match was not
// actually an identifier (e.g., matched inside a comment or string).
func isNoIdentifierError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no identifier found")
}

// getSymbolOffset returns the character offset to the actual symbol name
// in a qualified symbol (e.g., "Bar" in "foo.Bar" or "method" in "Class::method").
func getSymbolOffset(symbol string) int {
	if idx := strings.LastIndex(symbol, "::"); idx != -1 {
		return idx + 2
	}
	if idx := strings.LastIndex(symbol, "."); idx != -1 {
		return idx + 1
	}
	if idx := strings.LastIndex(symbol, "\\"); idx != -1 {
		return idx + 1
	}
	return 0
}

// cleanupLocations deduplicates and sorts a slice of LSP locations.
func cleanupLocations(locations []protocol.Location) []protocol.Location {
	slices.SortFunc(locations, func(a, b protocol.Location) int {
		if a.URI != b.URI {
			return strings.Compare(string(a.URI), string(b.URI))
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return cmp.Compare(a.Range.Start.Line, b.Range.Start.Line)
		}
		return cmp.Compare(a.Range.Start.Character, b.Range.Start.Character)
	})
	return slices.CompactFunc(locations, func(a, b protocol.Location) bool {
		return a.URI == b.URI &&
			a.Range.Start.Line == b.Range.Start.Line &&
			a.Range.Start.Character == b.Range.Start.Character
	})
}

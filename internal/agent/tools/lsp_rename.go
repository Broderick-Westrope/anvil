package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/fantasy"

	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	lsputil "github.com/Broderick-Westrope/anvil/internal/lsp/util"
	"github.com/Broderick-Westrope/anvil/internal/permission"
)

type RenameParams struct {
	Symbol   string `json:"symbol" description:"The symbol name to rename"`
	NewName  string `json:"new_name" description:"The new name for the symbol"`
	FilePath string `json:"file_path,omitempty" description:"The file containing the symbol. Strongly recommended when the symbol name may exist in multiple places."`
}

const RenameToolName = "lsp_rename"

//go:embed lsp_rename.md
var renameDescription string

func NewRenameTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	filetracker filetracker.Service,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		RenameToolName,
		renameDescription,
		func(ctx context.Context, params RenameParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Symbol == "" {
				return fantasy.NewTextErrorResponse("symbol is required"), nil
			}
			if params.NewName == "" {
				return fantasy.NewTextErrorResponse("new_name is required"), nil
			}

			var resolved *resolvedSymbol
			if params.FilePath != "" {
				var errResp *fantasy.ToolResponse
				resolved, errResp = resolveSymbolInFile(ctx, lspManager, params.Symbol, params.FilePath)
				if errResp != nil {
					return *errResp, nil
				}
			} else {
				candidates, err := resolveSymbolCandidates(ctx, lspManager, params.Symbol, ".")
				if err != nil {
					return fantasy.NewTextResponse(fmt.Sprintf("Symbol '%s' not found", params.Symbol)), nil
				}
				if len(candidates) > 1 {
					return fantasy.NewTextErrorResponse(formatRenameCandidates(params.Symbol, candidates)), nil
				}
				resolved = candidates[0]
			}

			edit, err := resolved.client.Rename(ctx, resolved.path, resolved.line, resolved.char, params.NewName)
			if err != nil {
				slog.Error("Failed to rename symbol", "error", err, "symbol", params.Symbol)
				return fantasy.NewTextErrorResponse(fmt.Sprintf("rename failed: %s", err)), nil
			}
			if edit == nil {
				return fantasy.NewTextResponse(fmt.Sprintf("No rename edits generated for symbol '%s'", params.Symbol)), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID != "" && permissions != nil {
				granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
					SessionID:   sessionID,
					ToolName:    RenameToolName,
					Description: fmt.Sprintf("Rename '%s' to '%s'", params.Symbol, params.NewName),
					Input:       resolved.path,
				})
				if err != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("permission request failed: %w", err)
				}
				if !granted.Granted {
					return NewPermissionDeniedResponse(granted.Reason), nil
				}
			}

			affectedFiles := collectAffectedFiles(edit)

			encoding := resolved.client.GetOffsetEncoding()
			if err := lsputil.ApplyWorkspaceEdit(*edit, encoding); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to apply rename edits: %s", err)), nil
			}

			if filetracker != nil && sessionID != "" {
				for _, path := range affectedFiles {
					filetracker.RecordRead(ctx, sessionID, path)
				}
			}

			notifyLSPs(ctx, lspManager, "")

			text := formatRenameResult(params.Symbol, params.NewName, countEditsByFile(edit))
			if len(affectedFiles) > 0 {
				text += "\n" + getDiagnostics(affectedFiles[0], lspManager)
			}

			return fantasy.NewTextResponse(text), nil
		},
	)
}

// resolveSymbolInFile resolves a symbol within a single file via LSP
// document symbols. It returns a non-nil error response when resolution
// fails.
func resolveSymbolInFile(ctx context.Context, lspManager *lsp.Manager, symbol, filePath string) (*resolvedSymbol, *fantasy.ToolResponse) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf("invalid file_path: %s", err))
		return nil, &resp
	}

	lspManager.Start(ctx, absPath)

	client := findLSPClient(lspManager, absPath)
	if client == nil {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf("no LSP client handles file: %s", filePath))
		return nil, &resp
	}

	symbols, err := client.DocumentSymbols(ctx, absPath)
	if err != nil {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf("failed to get document symbols: %s", err))
		return nil, &resp
	}

	target := findSymbolByName(symbols, symbol)
	if target == nil {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf("symbol '%s' not found in %s", symbol, filePath))
		return nil, &resp
	}

	pos := symbolSelectionStart(target)
	return &resolvedSymbol{
		client: client,
		path:   absPath,
		line:   int(pos.Line) + 1,
		char:   int(pos.Character) + 1,
	}, nil
}

// formatRenameCandidates renders an ambiguity error listing every candidate
// location and instructing a retry with file_path.
func formatRenameCandidates(symbol string, candidates []*resolvedSymbol) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Symbol '%s' matches in multiple files:\n\n", symbol)
	for _, c := range candidates {
		fmt.Fprintf(&b, "  %s:%d\n", c.path, c.line)
	}
	b.WriteString("\nRetry with file_path set to the file containing the symbol you want to rename.")
	return b.String()
}

// formatRenameResult renders per-file text-edit counts from a rename.
func formatRenameResult(symbol, newName string, counts map[string]int) string {
	paths := slices.Sorted(maps.Keys(counts))

	var b strings.Builder
	fmt.Fprintf(&b, "Renamed '%s' to '%s' in %d file(s):\n\n", symbol, newName, len(paths))
	for _, p := range paths {
		noun := "renames"
		if counts[p] == 1 {
			noun = "rename"
		}
		fmt.Fprintf(&b, "  %s: %d %s\n", p, counts[p], noun)
	}
	return b.String()
}

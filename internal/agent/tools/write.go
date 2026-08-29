package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/diff"
	"github.com/Broderick-Westrope/anvil/internal/filepathext"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/fsext"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/Broderick-Westrope/anvil/internal/permission"
)

//go:embed write.md
var writeDescription string

type WriteParams struct {
	FilePath string `json:"file_path" description:"The path to the file to write"`
	Content  string `json:"content" description:"The content to write to the file"`
}

type WritePermissionsParams struct {
	FilePath   string `json:"file_path"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

type WriteResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

const WriteToolName = "write"

func NewWriteTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	tracker filetracker.Service,
	workingDir string,
) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		WriteToolName,
		writeDescription,
		func(ctx context.Context, params WriteParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.FilePath == "" {
				return fantasy.NewTextErrorResponse("file_path is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session_id is required")
			}

			filePath := filepathext.SmartJoin(workingDir, params.FilePath)

			oldContent := ""
			writeContent := params.Content

			fileInfo, err := os.Stat(filePath)
			if err == nil {
				if fileInfo.IsDir() {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
				}

				diskBytes, readErr := os.ReadFile(filePath)
				if readErr != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("error reading file: %w", readErr)
				}

				diskHash := filetracker.HashContent(diskBytes)
				if tracker.LastContentHash(ctx, sessionID, filePath) != diskHash {
					// The gate error embeds file content, which is
					// equivalent to a view. For files outside the working
					// directory, require the same read permission the view
					// tool does before returning content.
					outside, outsideErr := isOutsideWorkingDir(filePath, workingDir)
					if outsideErr != nil {
						return fantasy.ToolResponse{}, outsideErr
					}
					if outside {
						granted, permErr := permissions.Request(ctx,
							permission.CreatePermissionRequest{
								SessionID:   sessionID,
								Path:        filePath,
								ToolCallID:  call.ID,
								ToolName:    WriteToolName,
								Action:      "read",
								Description: fmt.Sprintf("Read file outside working directory: %s", filePath),
								Params:      WritePermissionsParams{FilePath: filePath},
								Input:       filePath,
							},
						)
						if permErr != nil {
							return fantasy.ToolResponse{}, permErr
						}
						if !granted.Granted {
							return fantasy.NewTextErrorResponse(writeGateErrorWithoutContent(filePath)), nil
						}
					}

					// Record the disk hash so the failed call itself
					// counts as a read: an immediate retry passes the
					// gate.
					tracker.RecordReadWithHash(ctx, sessionID, filePath, diskHash)
					return fantasy.NewTextErrorResponse(writeGateError(filePath, diskBytes)), nil
				}

				oldContent = string(diskBytes)
				if _, isCrlf := fsext.ToUnixLineEndings(oldContent); isCrlf {
					writeContent, _ = fsext.ToWindowsLineEndings(writeContent)
				}

				if oldContent == writeContent {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("File %s already contains the exact content. No changes made.", filePath)), nil
				}
			} else if !os.IsNotExist(err) {
				return fantasy.ToolResponse{}, fmt.Errorf("error checking file: %w", err)
			}

			dir := filepath.Dir(filePath)
			if err = os.MkdirAll(dir, 0o755); err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error creating directory: %w", err)
			}

			diffText, additions, removals := diff.GenerateDiff(
				oldContent,
				writeContent,
				strings.TrimPrefix(filePath, workingDir),
			)

			p, err := permissions.Request(ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        fsext.PathOrPrefix(filePath, workingDir),
					ToolCallID:  call.ID,
					ToolName:    WriteToolName,
					Action:      "write",
					Description: fmt.Sprintf("Create file %s", filePath),
					Params: WritePermissionsParams{
						FilePath:   filePath,
						OldContent: oldContent,
						NewContent: writeContent,
					},
					Input: filePath,
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p.Granted {
				resp := NewPermissionDeniedResponse(p.Reason)
				resp = fantasy.WithResponseMetadata(resp, WriteResponseMetadata{
					Diff:      diffText,
					Additions: additions,
					Removals:  removals,
				})
				return resp, nil
			}

			err = os.WriteFile(filePath, []byte(writeContent), 0o644)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error writing file: %w", err)
			}

			tracker.RecordReadWithHash(ctx, sessionID, filePath, filetracker.HashContent([]byte(writeContent)))

			notifyLSPs(ctx, lspManager, params.FilePath)

			result := withDiff(fmt.Sprintf("File successfully written: %s", filePath), diffText)
			result = fmt.Sprintf("<result>\n%s\n</result>", result)
			result += getDiagnostics(filePath, lspManager)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result),
				WriteResponseMetadata{
					Diff:      diffText,
					Additions: additions,
					Removals:  removals,
				},
			), nil
		})
}

// isOutsideWorkingDir reports whether filePath resolves outside workingDir.
func isOutsideWorkingDir(filePath, workingDir string) (bool, error) {
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return false, fmt.Errorf("error resolving working directory: %w", err)
	}

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return false, fmt.Errorf("error resolving file path: %w", err)
	}

	relPath, err := filepath.Rel(absWorkingDir, absFilePath)
	return err != nil || strings.HasPrefix(relPath, ".."), nil
}

// writeGateErrorWithoutContent builds the gate error used when read
// permission for the file's content was denied: no content is embedded and
// the failed call does not count as a read.
func writeGateErrorWithoutContent(filePath string) string {
	return fmt.Sprintf("File %s has not been seen this session, or has changed on disk since it was last seen. Permission to read its content was denied, so the write cannot proceed. If you believe you should have access, ask the user or try the View tool.", filePath)
}

// writeGateError builds the error message returned when the write gate
// blocks an overwrite. It embeds the current file content, capped the same
// way the view tool caps output, so the failed call delivers everything a
// view would.
func writeGateError(filePath string, diskBytes []byte) string {
	header := fmt.Sprintf("File %s has not been seen this session, or has changed on disk since it was last seen.", filePath)
	footer := "This counts as a read. Review the content above and re-issue the write if you still intend to overwrite it."

	if !utf8.Valid(diskBytes) {
		return fmt.Sprintf("%s\n\nThe current file content is binary and cannot be displayed.\n\n%s", header, footer)
	}

	return fmt.Sprintf("%s\n\nCurrent file content:\n\n%s\n\n%s", header, capViewContent(string(diskBytes)), footer)
}

// capViewContent caps content the same way the view tool caps output: at
// most DefaultReadLimit lines, each at most MaxLineLength bytes, with a
// truncation note when capped.
func capViewContent(content string) string {
	lines := strings.Split(content, "\n")
	truncated := false
	if len(lines) > DefaultReadLimit {
		lines = lines[:DefaultReadLimit]
		truncated = true
	}
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if len(line) > MaxLineLength {
			line = strings.ToValidUTF8(line[:MaxLineLength], "") + "..."
		}
		lines[i] = line
	}
	out := strings.Join(lines, "\n")
	if truncated {
		out += fmt.Sprintf("\n\n(Content truncated at %d lines.)", DefaultReadLimit)
	}
	return out
}

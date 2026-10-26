package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/filepathext"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/permission"
)

type ValidateMermaidParams struct {
	Source   string `json:"source,omitempty" description:"One diagram's Mermaid source, without the fence lines. Mutually exclusive with file_path."`
	FilePath string `json:"file_path,omitempty" description:"A file to check: every top-level mermaid fence in a markdown file, or the whole file for .mmd and .mermaid. Mutually exclusive with source."`
}

const ValidateMermaidToolName = "validate_mermaid"

const maxValidateFileSize = 1 << 20

//go:embed validate_mermaid.md
var validateMermaidDescription string

// NewValidateMermaidTool returns a tool that renders Mermaid diagrams with
// the renderer the chat uses and reports any that fail. It reads files
// under the same rules as view, which takes the same trustedReadDirs and
// skillsPaths.
func NewValidateMermaidTool(permissions permission.Service, workingDir string, trustedReadDirs []string, skillsPaths ...string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ValidateMermaidToolName,
		validateMermaidDescription,
		func(ctx context.Context, params ValidateMermaidParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if (params.Source == "") == (params.FilePath == "") {
				return fantasy.NewTextErrorResponse("Pass exactly one of source or file_path."), nil
			}
			if params.Source != "" {
				report, err := mermaid.CheckMarkdown(ctx, strings.TrimSpace(params.Source)+"\n")
				if err == nil && report.Diagrams == 0 {
					report, err = mermaid.CheckDiagram(ctx, withoutOpeningFence(params.Source))
				}
				return reportResponse(report, err)
			}

			absPath, err := filepath.Abs(filepathext.SmartJoin(workingDir, params.FilePath))
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("resolve file path: %w", err)
			}
			resp, ok, err := ensureReadAllowed(ctx, call, ViewPermissionsParams{FilePath: absPath}, absPath, workingDir, skillsPaths, trustedReadDirs, permissions)
			if err != nil || !ok {
				return resp, err
			}
			content, errResp := readValidateFile(absPath)
			if errResp != nil {
				return *errResp, nil
			}
			var report mermaid.Report
			switch strings.ToLower(filepath.Ext(absPath)) {
			case ".mmd", ".mermaid":
				report, err = mermaid.CheckDiagram(ctx, content)
			default:
				report, err = mermaid.CheckMarkdown(ctx, content)
			}
			return reportResponse(report, err)
		})
}

func readValidateFile(path string) (string, *fantasy.ToolResponse) {
	fail := func(format string, args ...any) (string, *fantasy.ToolResponse) {
		resp := fantasy.NewTextErrorResponse(fmt.Sprintf(format, args...))
		return "", &resp
	}
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return fail("File not found: %s", path)
	case err != nil:
		return fail("Can't read %s: %v", path, err)
	case info.IsDir():
		return fail("Path is a directory, not a file: %s", path)
	case info.Size() > maxValidateFileSize:
		return fail("File is too large (%d bytes). Maximum size is %d bytes.", info.Size(), maxValidateFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail("Can't read %s: %v", path, err)
	}
	return string(data), nil
}

func reportResponse(report mermaid.Report, err error) (fantasy.ToolResponse, error) {
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	return fantasy.NewTextResponse(report.String()), nil
}

func withoutOpeningFence(source string) string {
	trimmed := strings.TrimLeft(source, " \t\r\n")
	if !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "~~~") {
		return source
	}
	_, rest, _ := strings.Cut(trimmed, "\n")
	return rest
}

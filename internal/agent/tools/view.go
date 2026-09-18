package tools

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/filepathext"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/tidwall/sjson"
)

//go:embed view.md.tpl
var viewDescriptionTmpl []byte

var viewDescriptionTpl = template.Must(
	template.New("viewDescription").
		Parse(string(viewDescriptionTmpl)),
)

type viewDescriptionData struct {
	DefaultReadLimit int
	MaxViewSizeKB    int
}

func viewDescription() string {
	return renderTemplate(viewDescriptionTpl, viewDescriptionData{
		DefaultReadLimit: DefaultReadLimit,
		MaxViewSizeKB:    MaxViewSize / 1024,
	})
}

type ViewParams struct {
	FilePath  string `json:"file_path,omitempty" description:"The path to the file to read. Mutually exclusive with skill_name."`
	SkillName string `json:"skill_name,omitempty" description:"Exact name of an enabled skill to load in full. Mutually exclusive with file_path. offset and limit are not supported."`
	Offset    int    `json:"offset,omitempty" description:"The line number to start reading from (0-based)"`
	Limit     int    `json:"limit,omitempty" description:"The number of lines to read (defaults to 200)"`
}

type ViewPermissionsParams struct {
	FilePath  string `json:"file_path"`
	SkillName string `json:"skill_name"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
}

type ViewResourceType string

const (
	ViewResourceUnset ViewResourceType = ""
	ViewResourceSkill ViewResourceType = "skill"
)

type ViewResponseMetadata struct {
	FilePath            string           `json:"file_path"`
	Content             string           `json:"content"`
	ResourceType        ViewResourceType `json:"resource_type,omitempty"`
	ResourceName        string           `json:"resource_name,omitempty"`
	ResourceDescription string           `json:"resource_description,omitempty"`
}

const (
	ViewToolName     = "view"
	MaxViewSize      = 200 * 1024 // 200KB
	DefaultReadLimit = 200
	MaxLineLength    = 2000

	MaxSkillLoadSize = 1024 * 1024

	skillLoadModeName = "name"
	skillLoadModePath = "path"
	skillLoadModeNone = "none"
)

const errSelectorRequired = "pass exactly one of file_path or skill_name"

var ErrPathToNameRewrite = errors.New(
	"PreToolUse hooks may not convert a file_path read into a skill_name load")

var errNotRegularSource = errors.New("skill source is not a regular file")

type contentTooLargeError struct {
	Size int
	Max  int
}

func (e contentTooLargeError) Error() string {
	return fmt.Sprintf("content section is too large (%d bytes). Maximum size is %d bytes", e.Size, e.Max)
}

type viewTool struct {
	fantasy.AgentTool
	registry   []*skills.Skill
	workingDir string
}

func NewViewTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	filetracker filetracker.Service,
	skillTracker *skills.Tracker,
	skillRegistry []*skills.Skill,
	workingDir string,
	skillsPaths ...string,
) fantasy.AgentTool {
	vt := &viewTool{registry: skillRegistry, workingDir: workingDir}
	vt.AgentTool = fantasy.NewAgentTool(
		ViewToolName,
		viewDescription(),
		func(ctx context.Context, params ViewParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			baseline, hasBaseline := GetSkillLoadBaseline(ctx)

			if params.SkillName != "" && hasBaseline &&
				(baseline.Mode == skillLoadModePath || baseline.Mode == skillLoadModeNone) {
				return fantasy.NewTextErrorResponse(ErrPathToNameRewrite.Error()), nil
			}

			if hasBaseline && baseline.Mode == skillLoadModeName {
				target, err := vt.CanonicalTarget(ctx, call.Input)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				switch target.Mode {
				case skillLoadModeName:
					return runSkillNameMode(ctx, call, ViewParams{SkillName: target.Name, Offset: params.Offset, Limit: params.Limit}, vt.registry, skillTracker, filetracker, permissions, workingDir, skillsPaths)
				case skillLoadModePath:
					return runPathMode(ctx, call, ViewParams{FilePath: target.Location, Offset: params.Offset, Limit: params.Limit}, lspManager, permissions, filetracker, skillTracker, workingDir, skillsPaths)
				default:
					return fantasy.NewTextErrorResponse(errSelectorRequired), nil
				}
			}

			switch {
			case params.SkillName != "" && params.FilePath != "":
				return fantasy.NewTextErrorResponse(errSelectorRequired), nil
			case params.SkillName != "":
				return runSkillNameMode(ctx, call, params, vt.registry, skillTracker, filetracker, permissions, workingDir, skillsPaths)
			case params.FilePath != "":
				return runPathMode(ctx, call, params, lspManager, permissions, filetracker, skillTracker, workingDir, skillsPaths)
			default:
				return fantasy.NewTextErrorResponse(errSelectorRequired), nil
			}
		},
	)
	return vt
}

func runPathMode(
	ctx context.Context,
	call fantasy.ToolCall,
	params ViewParams,
	lspManager *lsp.Manager,
	permissions permission.Service,
	filetracker filetracker.Service,
	skillTracker *skills.Tracker,
	workingDir string,
	skillsPaths []string,
) (fantasy.ToolResponse, error) {
	if strings.HasPrefix(params.FilePath, skills.BuiltinPrefix) {
		return readBuiltinFile(params, skillTracker)
	}

	filePath := filepathext.SmartJoin(workingDir, params.FilePath)

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("error resolving file path: %w", err)
	}

	resp, ok, err := ensureReadAllowed(ctx, call, ViewPermissionsParams(params), absFilePath, workingDir, skillsPaths, permissions)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if !ok {
		return resp, nil
	}
	isSkillFile := isInSkillsPath(absFilePath, skillsPaths)

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			dir := filepath.Dir(filePath)
			base := filepath.Base(filePath)

			dirEntries, dirErr := os.ReadDir(dir)
			if dirErr == nil {
				var suggestions []string
				for _, entry := range dirEntries {
					if strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(base)) ||
						strings.Contains(strings.ToLower(base), strings.ToLower(entry.Name())) {
						suggestions = append(suggestions, filepath.Join(dir, entry.Name()))
						if len(suggestions) >= 3 {
							break
						}
					}
				}

				if len(suggestions) > 0 {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("File not found: %s\n\nDid you mean one of these?\n%s",
						filePath, strings.Join(suggestions, "\n"))), nil
				}
			}

			return fantasy.NewTextErrorResponse(fmt.Sprintf("File not found: %s", filePath)), nil
		}
		return fantasy.ToolResponse{}, fmt.Errorf("error accessing file: %w", err)
	}

	if fileInfo.IsDir() {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
	}

	if params.Limit <= 0 {
		if isSkillFile {
			params.Limit = 1000000 // Effectively no limit for skill files
		} else {
			params.Limit = DefaultReadLimit
		}
	}

	isSupportedImage, mimeType := getImageMimeType(filePath)
	if isSupportedImage {
		if fileInfo.Size() > MaxViewSize {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("Image file is too large (%d bytes). Maximum size is %d bytes",
				fileInfo.Size(), MaxViewSize)), nil
		}
		if !GetSupportsImagesFromContext(ctx) {
			modelName := GetModelNameFromContext(ctx)
			return fantasy.NewTextErrorResponse(fmt.Sprintf("This model (%s) does not support image data.", modelName)), nil
		}

		imageData, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return fantasy.ToolResponse{}, fmt.Errorf("error reading image file: %w", readErr)
		}

		mimeType = sniffImageMimeType(imageData, mimeType)

		return fantasy.NewImageResponse(imageData, mimeType), nil
	}

	maxContentSize := MaxViewSize
	if isSkillFile {
		maxContentSize = 0
	}
	content, hasMore, err := readTextFile(filePath, params.Offset, params.Limit, maxContentSize)
	if err != nil {
		var tooLarge contentTooLargeError
		if errors.As(err, &tooLarge) {
			return fantasy.NewTextErrorResponse(fmt.Sprintf("Content section is too large (%d bytes). Maximum size is %d bytes",
				tooLarge.Size, tooLarge.Max)), nil
		}
		return fantasy.ToolResponse{}, fmt.Errorf("error reading file: %w", err)
	}
	if !utf8.ValidString(content) {
		return fantasy.NewTextErrorResponse("File content is not valid UTF-8"), nil
	}

	sessionID := GetSessionFromContext(ctx)
	openInLSPs(ctx, lspManager, filePath)
	waitForLSPDiagnostics(ctx, lspManager, filePath, 300*time.Millisecond)
	output := "<file>\n"
	output += addLineNumbers(content, params.Offset+1)

	if hasMore {
		output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
			params.Offset+len(strings.Split(content, "\n")))
	}
	output += "\n</file>\n"
	output += getDiagnostics(filePath, lspManager)
	filetracker.RecordRead(ctx, sessionID, filePath)

	meta := ViewResponseMetadata{
		FilePath: filePath,
		Content:  content,
	}
	if isSkillFile {
		if skill, err := skills.Parse(filePath); err == nil {
			meta.ResourceType = ViewResourceSkill
			meta.ResourceName = skill.Name
			meta.ResourceDescription = skill.Description
			skillTracker.MarkLoaded(skill.Name)
		}
	}

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(output),
		meta,
	), nil
}

func ensureReadAllowed(
	ctx context.Context,
	call fantasy.ToolCall,
	permParams any,
	absPath string,
	workingDir string,
	skillsPaths []string,
	permissions permission.Service,
) (fantasy.ToolResponse, bool, error) {
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return fantasy.ToolResponse{}, false, fmt.Errorf("error resolving working directory: %w", err)
	}

	relPath, err := filepath.Rel(absWorkingDir, absPath)
	isOutsideWorkDir := err != nil || strings.HasPrefix(relPath, "..")
	isSkillFile := isInSkillsPath(absPath, skillsPaths)

	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return fantasy.ToolResponse{}, false, fmt.Errorf("session ID is required for accessing files outside working directory")
	}

	if isOutsideWorkDir && !isSkillFile {
		granted, permReqErr := permissions.Request(ctx,
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        absPath,
				ToolCallID:  call.ID,
				ToolName:    ViewToolName,
				Action:      "read",
				Description: fmt.Sprintf("Read file outside working directory: %s", absPath),
				Params:      permParams,
				Input:       absPath,
			},
		)
		if permReqErr != nil {
			return fantasy.ToolResponse{}, false, permReqErr
		}
		if !granted.Granted {
			return NewPermissionDeniedResponse(granted.Reason), false, nil
		}
	}

	return fantasy.ToolResponse{}, true, nil
}

func runSkillNameMode(
	ctx context.Context,
	call fantasy.ToolCall,
	params ViewParams,
	registry []*skills.Skill,
	skillTracker *skills.Tracker,
	filetracker filetracker.Service,
	permissions permission.Service,
	workingDir string,
	skillsPaths []string,
) (fantasy.ToolResponse, error) {
	if params.Offset != 0 || params.Limit != 0 {
		return fantasy.NewTextErrorResponse(
			"offset and limit are not supported with skill_name; the full skill body is always returned",
		), nil
	}

	located, err := skills.Lookup(registry, params.SkillName)
	if err != nil {
		switch {
		case errors.Is(err, skills.ErrNotInRegistry):
			return fantasy.NewTextErrorResponse(fmt.Sprintf(
				"Skill %q is not available in this registry snapshot. Check the exact name, or read the file directly with file_path.",
				params.SkillName,
			)), nil
		case errors.Is(err, skills.ErrNoLocation):
			return fantasy.NewTextErrorResponse(fmt.Sprintf(
				"Skill %q has no recorded location in this registry snapshot.", params.SkillName,
			)), nil
		default:
			return fantasy.ToolResponse{}, err
		}
	}

	if located.Builtin {
		return loadBuiltinSkillByName(located, params.SkillName, skillTracker)
	}

	permParams := ViewPermissionsParams(params)
	permParams.FilePath = located.Location
	resp, ok, err := ensureReadAllowed(ctx, call, permParams, located.Location, workingDir, skillsPaths, permissions)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if !ok {
		return resp, nil
	}

	return loadDiskSkillByName(ctx, located, params.SkillName, skillTracker, filetracker)
}

func loadBuiltinSkillByName(located skills.Located, requestedName string, skillTracker *skills.Tracker) (fantasy.ToolResponse, error) {
	embeddedPath := "builtin/" + strings.TrimPrefix(located.Location, skills.BuiltinPrefix)
	data, err := fs.ReadFile(skills.BuiltinFS(), embeddedPath)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Builtin skill file not found: %s", located.Location)), nil
	}
	if len(data) > MaxSkillLoadSize {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"Skill %q is too large (%d bytes). Maximum size is %d bytes", requestedName, len(data), MaxSkillLoadSize,
		)), nil
	}
	return buildSkillLoadResponse(data, located, requestedName, skillTracker)
}

func loadDiskSkillByName(
	ctx context.Context,
	located skills.Located,
	requestedName string,
	skillTracker *skills.Tracker,
	tracker filetracker.Service,
) (fantasy.ToolResponse, error) {
	f, err := openRegularFile(located.Location)
	if err != nil {
		switch {
		case errors.Is(err, errNotRegularSource):
			return fantasy.NewTextErrorResponse(fmt.Sprintf(
				"Skill %q source is not a regular file: %s", requestedName, located.Location,
			)), nil
		case errors.Is(err, fs.ErrNotExist):
			return fantasy.NewTextErrorResponse(fmt.Sprintf(
				"Skill %q source not found: %s", requestedName, located.Location,
			)), nil
		case errors.Is(err, fs.ErrPermission):
			return fantasy.NewTextErrorResponse(fmt.Sprintf(
				"Skill %q source is not readable: %s", requestedName, located.Location,
			)), nil
		default:
			return fantasy.ToolResponse{}, fmt.Errorf("opening skill file: %w", err)
		}
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxSkillLoadSize+1))
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("reading skill file: %w", err)
	}
	if len(data) > MaxSkillLoadSize {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"Skill %q is too large (over %d bytes). Maximum size is %d bytes", requestedName, MaxSkillLoadSize, MaxSkillLoadSize,
		)), nil
	}

	resp, err := buildSkillLoadResponse(data, located, requestedName, skillTracker)
	if err == nil && !resp.IsError {
		tracker.RecordReadWithHash(ctx, GetSessionFromContext(ctx), located.Location, filetracker.HashContent(data))
	}
	return resp, err
}

func buildSkillLoadResponse(data []byte, located skills.Located, requestedName string, skillTracker *skills.Tracker) (fantasy.ToolResponse, error) {
	if !utf8.Valid(data) {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Skill %q content is not valid UTF-8", requestedName)), nil
	}

	parsed, err := skills.ParseContent(data)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Skill %q has malformed frontmatter: %s", requestedName, err)), nil
	}
	if err := parsed.Validate(); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Skill %q failed validation: %s", requestedName, err)), nil
	}
	if parsed.Name != requestedName {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"Skill file at %s declares name %q, not the requested %q", located.Location, parsed.Name, requestedName,
		)), nil
	}

	skillTracker.MarkLoaded(parsed.Name)

	content := string(data)
	baseDir := located.BaseDir()
	var trailer string
	if located.Builtin {
		trailer = fmt.Sprintf(
			"This skill is embedded in the Anvil binary. Read its assets with file_path values like %s/reference.md. They are not files on disk and its scripts cannot be executed.",
			baseDir,
		)
	} else {
		trailer = fmt.Sprintf("References, scripts and assets in this skill resolve relative to %s.", baseDir)
	}

	output := fmt.Sprintf("<skill name=%q location=%q>\n%s\n</skill>\n\n%s", parsed.Name, located.Location, content, trailer)

	meta := ViewResponseMetadata{
		FilePath:            located.Location,
		Content:             content,
		ResourceType:        ViewResourceSkill,
		ResourceName:        parsed.Name,
		ResourceDescription: parsed.Description,
	}

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(output),
		meta,
	), nil
}

type HookTargetResolver interface {
	PrepareHookInput(ctx context.Context, input string) (string, context.Context, error)

	CanonicalTarget(ctx context.Context, input string) (HookTarget, error)
}

type HookTarget struct {
	Mode     string
	Name     string
	Location string
	Resolved bool
}

var ErrAmbiguousRewrite = errors.New(
	"both skill_name and file_path were rewritten by a hook to different targets")

func (vt *viewTool) PrepareHookInput(ctx context.Context, input string) (string, context.Context, error) {
	var params ViewParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return input, WithSkillLoadBaseline(ctx, SkillLoadBaseline{Mode: skillLoadModeNone}), nil
	}

	switch {
	case params.SkillName != "" && params.FilePath == "":
		located, err := skills.Lookup(vt.registry, params.SkillName)
		if err != nil {
			baseline := SkillLoadBaseline{Mode: skillLoadModeName, Name: params.SkillName, Resolved: false}
			return input, WithSkillLoadBaseline(ctx, baseline), nil
		}
		baseline := SkillLoadBaseline{
			Mode:     skillLoadModeName,
			Name:     params.SkillName,
			Location: located.Location,
			Resolved: true,
		}
		prepared, err := sjson.Set(input, "file_path", located.Location)
		if err != nil {
			baseline.Resolved = false
			return input, WithSkillLoadBaseline(ctx, baseline), err
		}
		return prepared, WithSkillLoadBaseline(ctx, baseline), nil
	case params.FilePath != "" && params.SkillName == "":
		return input, WithSkillLoadBaseline(ctx, SkillLoadBaseline{Mode: skillLoadModePath}), nil
	default:
		return input, WithSkillLoadBaseline(ctx, SkillLoadBaseline{Mode: skillLoadModeNone}), nil
	}
}

func (vt *viewTool) canonicalPathLocation(filePath string) string {
	if strings.HasPrefix(filePath, skills.BuiltinPrefix) {
		return filePath
	}
	joined := filepathext.SmartJoin(vt.workingDir, filePath)
	abs, err := filepath.Abs(joined)
	if err != nil {
		return joined
	}
	return abs
}

func (vt *viewTool) canonicalTargetForName(name string) HookTarget {
	located, err := skills.Lookup(vt.registry, name)
	if err != nil {
		return HookTarget{Mode: skillLoadModeName, Name: name, Resolved: false}
	}
	return HookTarget{Mode: skillLoadModeName, Name: name, Location: located.Location, Resolved: true}
}

func (vt *viewTool) CanonicalTarget(ctx context.Context, input string) (HookTarget, error) {
	baseline, hasBaseline := GetSkillLoadBaseline(ctx)

	var params ViewParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return HookTarget{Mode: skillLoadModeNone}, nil
	}

	if !hasBaseline || baseline.Mode != skillLoadModeName {
		if params.SkillName != "" {
			return HookTarget{}, ErrPathToNameRewrite
		}
		if params.FilePath == "" {
			return HookTarget{Mode: skillLoadModeNone}, nil
		}
		return HookTarget{Mode: skillLoadModePath, Location: vt.canonicalPathLocation(params.FilePath), Resolved: true}, nil
	}

	nameCleared := params.SkillName == ""
	nameSame := !nameCleared && params.SkillName == baseline.Name

	pathCleared := params.FilePath == ""
	pathSame := !pathCleared && vt.canonicalPathLocation(params.FilePath) == baseline.Location
	pathChanged := !pathCleared && !pathSame

	switch {
	case nameCleared && pathCleared:
		return HookTarget{Mode: skillLoadModeNone}, nil
	case nameCleared:
		return HookTarget{Mode: skillLoadModePath, Location: vt.canonicalPathLocation(params.FilePath), Resolved: true}, nil
	case nameSame && pathChanged:
		return HookTarget{Mode: skillLoadModePath, Location: vt.canonicalPathLocation(params.FilePath), Resolved: true}, nil
	case nameSame:
		return HookTarget{Mode: skillLoadModeName, Name: params.SkillName, Location: baseline.Location, Resolved: baseline.Resolved}, nil
	case pathSame:
		return vt.canonicalTargetForName(params.SkillName), nil
	case pathCleared:
		return vt.canonicalTargetForName(params.SkillName), nil
	default:
		newTarget := vt.canonicalTargetForName(params.SkillName)
		if newTarget.Resolved && newTarget.Location == vt.canonicalPathLocation(params.FilePath) {
			return newTarget, nil
		}
		return HookTarget{}, ErrAmbiguousRewrite
	}
}

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

	var result []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")

		lineNum := i + startLine
		numStr := fmt.Sprintf("%d", lineNum)

		if len(numStr) >= 6 {
			result = append(result, fmt.Sprintf("%s|%s", numStr, line))
		} else {
			paddedNum := fmt.Sprintf("%6s", numStr)
			result = append(result, fmt.Sprintf("%s|%s", paddedNum, line))
		}
	}

	return strings.Join(result, "\n")
}

func readTextFile(filePath string, offset, limit, maxContentSize int) (string, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", false, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	skipped := 0
	for skipped < offset {
		_, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return "", false, nil
			}
			return "", false, err
		}
		skipped++
	}

	lines := make([]string, 0, min(limit, DefaultReadLimit))
	contentSize := 0

	for len(lines) < limit {
		lineText, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", false, err
		}
		lineText = strings.TrimSuffix(lineText, "\n")
		lineText = strings.TrimSuffix(lineText, "\r")
		if len(lineText) > MaxLineLength {
			// The byte-boundary cut can split a multi-byte
			// character; ToValidUTF8 heals the tail by dropping
			// the partial sequence.
			lineText = strings.ToValidUTF8(lineText[:MaxLineLength], "") + "..."
		}
		projectedSize := contentSize + len(lineText)
		if len(lines) > 0 {
			projectedSize++
		}
		if maxContentSize > 0 && projectedSize > maxContentSize {
			return "", false, contentTooLargeError{Size: projectedSize, Max: maxContentSize}
		}
		contentSize = projectedSize
		lines = append(lines, lineText)
		if err == io.EOF {
			break
		}
	}

	// Peek one more line only when we filled the limit.
	hasMore := false
	if len(lines) == limit {
		lineText, peekErr := reader.ReadString('\n')
		hasMore = len(lineText) > 0 || peekErr == nil
	}

	return strings.Join(lines, "\n"), hasMore, nil
}

func getImageMimeType(filePath string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return true, "image/jpeg"
	case ".png":
		return true, "image/png"
	case ".gif":
		return true, "image/gif"
	case ".webp":
		return true, "image/webp"
	default:
		return false, ""
	}
}

// sniffImageMimeType returns the content-sniffed MIME type when it identifies
// a supported image format. Otherwise it returns the provided fallback, which
// is usually the extension-derived type. Providers that validate the image
// media type against the base64 magic bytes (e.g. Anthropic) reject mismatched
// requests with a 400, so trusting the filename alone is unsafe.
func sniffImageMimeType(data []byte, fallback string) string {
	sniffed := http.DetectContentType(data)
	// http.DetectContentType may return the MIME with a ";" parameter
	// (e.g. "image/svg+xml; charset=utf-8") although current image sniffers
	// return bare types; strip defensively.
	if i := strings.IndexByte(sniffed, ';'); i >= 0 {
		sniffed = strings.TrimSpace(sniffed[:i])
	}
	switch sniffed {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return sniffed
	}
	return fallback
}

// isInSkillsPath checks if filePath is within any of the configured skills
// directories. Returns true for files that can be read without permission
// prompts and without size limits.
//
// Note that symlinks are resolved to prevent path traversal attacks via
// symbolic links.
func isInSkillsPath(filePath string, skillsPaths []string) bool {
	if len(skillsPaths) == 0 {
		return false
	}

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}

	evalFilePath, err := filepath.EvalSymlinks(absFilePath)
	if err != nil {
		return false
	}

	for _, skillsPath := range skillsPaths {
		absSkillsPath, err := filepath.Abs(skillsPath)
		if err != nil {
			continue
		}

		evalSkillsPath, err := filepath.EvalSymlinks(absSkillsPath)
		if err != nil {
			continue
		}

		relPath, err := filepath.Rel(evalSkillsPath, evalFilePath)
		if err == nil && !strings.HasPrefix(relPath, "..") {
			return true
		}
	}

	return false
}

// readBuiltinFile reads a file from the embedded builtin skills filesystem.
func readBuiltinFile(params ViewParams, skillTracker *skills.Tracker) (fantasy.ToolResponse, error) {
	embeddedPath := "builtin/" + strings.TrimPrefix(params.FilePath, skills.BuiltinPrefix)
	builtinFS := skills.BuiltinFS()

	data, err := fs.ReadFile(builtinFS, embeddedPath)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Builtin file not found: %s", params.FilePath)), nil
	}

	content := string(data)
	if !utf8.ValidString(content) {
		return fantasy.NewTextErrorResponse("File content is not valid UTF-8"), nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 1000000 // Effectively no limit for skill files.
	}

	lines := strings.Split(content, "\n")
	offset := min(params.Offset, len(lines))
	lines = lines[offset:]

	hasMore := len(lines) > limit
	if hasMore {
		lines = lines[:limit]
	}

	output := "<file>\n"
	output += addLineNumbers(strings.Join(lines, "\n"), offset+1)
	if hasMore {
		output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
			offset+len(lines))
	}
	output += "\n</file>\n"

	meta := ViewResponseMetadata{
		FilePath: params.FilePath,
		Content:  strings.Join(lines, "\n"),
	}
	if skill, err := skills.ParseContent(data); err == nil {
		meta.ResourceType = ViewResourceSkill
		meta.ResourceName = skill.Name
		meta.ResourceDescription = skill.Description
		skillTracker.MarkLoaded(skill.Name)
	}

	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(output),
		meta,
	), nil
}

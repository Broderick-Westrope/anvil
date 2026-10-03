package assessor

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
)

const (
	maxFieldChars   = 300 // Per command segment / per message.
	maxInputChars   = 1500
	maxSegments     = 12
	maxMessages     = 3
	maxStateBytes   = 6000 // Whole marshalled state.
	maxMCPArgsChars = 1500
	maxContentChars = 1200 // Excerpt of new file content or diff for edits.
)

// Skip reasons returned by BuildState.
const (
	skipNotEligible     = "tool not eligible for assessment"
	skipInputTooLong    = "input too long"
	skipTooManySegments = "too many command segments"
	skipDynamicCommand  = "dynamic command name"
	skipProtectedPath   = "protected path"
	skipSensitivePath   = "sensitive path"
	skipOutsideWorkDir  = "path outside working directory"
	skipUnresolvedPath  = "could not resolve path"
	skipNoMCPArgs       = "mcp arguments missing"
	skipMCPArgsTooLong  = "mcp arguments too long"
	skipStateTooLarge   = "state too large"
	skipNoEditContents  = "edit contents unavailable"
)

// BuildState decides whether a call is eligible for assessment and builds
// the bounded, redacted state sent to the classifier. A non-empty skip
// reason means the call must go to the human without a classifier call.
func BuildState(in permission.AssessInput, sendUserMessages bool) (map[string]any, string) {
	state := map[string]any{
		"tool":   in.ToolName,
		"action": clean(in.Action, maxFieldChars),
	}
	if in.Description != "" {
		state["description"] = clean(in.Description, maxFieldChars)
	}

	var skip string
	switch {
	case in.ToolName == "bash":
		skip = bashState(state, in)
	case in.ToolName == "edit" || in.ToolName == "multiedit" || in.ToolName == "write":
		skip = editState(state, in)
	case in.ToolName == "view" || in.ToolName == "ls":
		skip = readState(state, in)
	case in.ToolName == "fetch" || in.ToolName == "download" || in.ToolName == "agentic_fetch" || in.ToolName == "web_fetch":
		state["url"] = clean(in.Input, maxFieldChars)
		state["network_hosts"] = hosts(in.Input)
		if in.ToolName == "download" {
			state["destination_path"] = clean(in.Path, maxFieldChars)
		}
	case strings.HasPrefix(in.ToolName, "mcp_"):
		skip = mcpState(state, in)
	default:
		skip = skipNotEligible
	}
	if skip != "" {
		return nil, skip
	}

	if sendUserMessages && len(in.RecentUserMessages) > 0 {
		msgs := in.RecentUserMessages
		if len(msgs) > maxMessages {
			msgs = msgs[len(msgs)-maxMessages:]
		}
		out := make([]string, len(msgs))
		for i, m := range msgs {
			out[i] = clean(m, maxFieldChars)
		}
		state["recent_user_messages"] = out
	}

	raw, err := json.Marshal(state)
	if err != nil || len(raw) > maxStateBytes {
		return nil, skipStateTooLarge
	}
	return state, ""
}

func bashState(state map[string]any, in permission.AssessInput) string {
	if utf8.RuneCountInString(in.Input) > maxInputChars {
		return skipInputTooLong
	}
	segs := segment.Normalized(in.Input)
	if len(segs) > maxSegments {
		return skipTooManySegments
	}

	commands := make([]string, 0, len(segs))
	executable := 0
	redirect := false
	for _, seg := range segs {
		if segment.IsRedirect(seg) {
			redirect = true
		} else {
			if dynamicCommandName(seg) {
				return skipDynamicCommand
			}
			executable++
		}
		commands = append(commands, clean(seg, maxFieldChars))
	}

	state["commands"] = commands
	state["executable_count"] = executable
	state["writes_via_redirect"] = redirect
	state["uses_command_substitution"] = strings.Contains(in.Input, "$(") || strings.Contains(in.Input, "`")
	state["network_hosts"] = hosts(in.Input)
	state["working_directory"] = clean(cmp.Or(in.Path, in.WorkingDir), maxFieldChars)
	return ""
}

// dynamicCommandName reports whether a segment's command name is only
// known at runtime. Deny rules can't see such commands, so the
// classifier must not be the one to approve them.
func dynamicCommandName(seg string) bool {
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return false
	}
	name := fields[0]
	if name == "[" {
		return false
	}
	escaped := false
	for _, r := range name {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == '$' || r == '`':
			return true
		case r == '*' || r == '?' || r == '[':
			return true
		}
	}
	return false
}

func editState(state map[string]any, in permission.AssessInput) string {
	target := absPath(in.Input, in.WorkingDir)
	resolved, err := resolve(target)
	if err != nil {
		return skipUnresolvedPath
	}
	if protectedPath(target) || protectedPath(resolved) {
		return skipProtectedPath
	}
	inside, unknown := isInside(target, in.WorkingDir)
	if unknown {
		return skipUnresolvedPath
	}
	if !inside {
		return skipOutsideWorkDir
	}

	if in.Diff == "" && in.Content == "" {
		return skipNoEditContents
	}

	state["target_path"] = clean(target, maxFieldChars)
	state["target_inside_working_directory"] = true
	if in.Diff != "" {
		added, removed := diffLineCounts(in.Diff)
		change := Redact(in.Diff)
		state["lines_added"] = added
		state["lines_removed"] = removed
		state["change_diff"] = truncate(change, maxContentChars)
		state["diff_truncated"] = utf8.RuneCountInString(change) > maxContentChars
		return ""
	}
	content := Redact(in.Content)
	state["new_content_excerpt"] = truncate(content, maxContentChars)
	state["content_truncated"] = utf8.RuneCountInString(content) > maxContentChars
	return ""
}

// diffLineCounts counts added and removed lines in a unified diff. File
// headers ("--- " and "+++ " before the first hunk) are not counted.
func diffLineCounts(d string) (added, removed int) {
	inHunk := false
	for line := range strings.SplitSeq(d, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func readState(state map[string]any, in permission.AssessInput) string {
	target := absPath(in.Input, in.WorkingDir)
	resolved, err := resolve(target)
	if err != nil {
		return skipUnresolvedPath
	}
	if sensitivePath(target) || sensitivePath(resolved) {
		return skipSensitivePath
	}
	inside, unknown := isInside(target, in.WorkingDir)
	if unknown {
		return skipUnresolvedPath
	}
	state["target_path"] = clean(target, maxFieldChars)
	state["target_inside_working_directory"] = inside
	return ""
}

func mcpState(state map[string]any, in permission.AssessInput) string {
	args := strings.TrimSpace(in.ArgsJSON)
	if args == "" {
		return skipNoMCPArgs
	}
	if utf8.RuneCountInString(args) > maxMCPArgsChars {
		return skipMCPArgsTooLong
	}
	state["mcp_tool"] = in.ToolName
	state["mcp_arguments"] = Redact(args)
	return ""
}

// clean redacts s, then truncates it to n runes. Redacting first means a
// secret cut in half by truncation is still caught.
func clean(s string, n int) string {
	return truncate(Redact(s), n)
}

var urlHostRe = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://([^/\s'"?#]+)`)

// hosts extracts the distinct hosts of URLs in s, in order of appearance.
func hosts(s string) []string {
	out := []string{}
	for _, m := range urlHostRe.FindAllStringSubmatch(s, -1) {
		host := m[1]
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		host = clean(host, maxFieldChars)
		if host != "" && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	return out
}

func absPath(p, wd string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if wd == "" {
			return ""
		}
		p = filepath.Join(wd, p)
	}
	return filepath.Clean(p)
}

// resolve applies filepath.EvalSymlinks to the nearest existing ancestor
// of p and rejoins the non-existent remainder, so paths that don't exist
// yet still resolve through any symlinked parent.
func resolve(p string) (string, error) {
	if p == "" || !filepath.IsAbs(p) {
		return "", errors.New("path is not absolute")
	}
	cur := filepath.Clean(p)
	var rest []string
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			slices.Reverse(rest)
			return filepath.Join(append([]string{real}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", errors.New("no existing ancestor")
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// isInside reports whether path is within wd after resolving symlinks.
// unknown is true when either path can't be resolved; callers must then
// treat the fact as unverified.
func isInside(path, wd string) (inside, unknown bool) {
	rp, err := resolve(path)
	if err != nil {
		return false, true
	}
	rw, err := resolve(wd)
	if err != nil {
		return false, true
	}
	rel, err := filepath.Rel(rw, rp)
	if err != nil {
		return false, true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, false
	}
	return true, false
}

var (
	protectedDirs  = []string{".git", ".anvil", ".husky"}
	protectedFiles = []string{
		"anvil.json", ".anvil.json", ".mcp.json", ".envrc", ".npmrc",
		".bashrc", ".zshrc", ".profile", ".zprofile", ".bash_profile", "config.fish",
	}
	protectedHomeDirs = []string{".ssh", ".aws", filepath.Join(".config", "gcloud")}
	sensitiveHomeDirs = []string{".ssh", ".aws", ".gnupg", filepath.Join(".config", "gcloud")}
)

// protectedPath reports whether an edit to p could change agent, VCS, CI,
// shell, or credential configuration.
func protectedPath(p string) bool {
	if p == "" {
		return true
	}
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i, part := range parts {
		if slices.Contains(protectedDirs, part) {
			return true
		}
		if part == ".github" && i+1 < len(parts) && parts[i+1] == "workflows" {
			return true
		}
	}
	if slices.Contains(protectedFiles, filepath.Base(p)) {
		return true
	}
	return underHome(p, protectedHomeDirs)
}

// sensitivePath reports whether reading p could expose credentials.
func sensitivePath(p string) bool {
	if p == "" {
		return true
	}
	base := filepath.Base(p)
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasPrefix(base, ".env") {
		return true
	}
	return underHome(p, sensitiveHomeDirs)
}

func underHome(p string, dirs []string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	homes := []string{filepath.Clean(home)}
	if real, err := filepath.EvalSymlinks(home); err == nil && real != homes[0] {
		homes = append(homes, real)
	}
	for _, h := range homes {
		for _, d := range dirs {
			root := filepath.Join(h, d)
			if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

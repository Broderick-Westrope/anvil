// Package match provides glob and brace-expansion pattern matching.
//
// Patterns support glob syntax (*, ?, [...]) and brace expansion
// ({a,b,c}) including nesting.
//
// The * wildcard matches any sequence of characters, including /.
// This mirrors permission-rule semantics in tools like OpenCode and
// Claude Code where patterns match bash commands, URLs, and file
// paths alike (so "git diff *" matches "git diff internal/foo.go"
// and "/tmp/*" matches "/tmp/sub/file.txt").
package match

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Match reports whether input matches the pattern. Brace expansion is
// applied first, then each expanded pattern is glob-matched. Returns
// true if any expanded pattern matches.
func Match(pattern, input string) (bool, error) {
	if err := checkBraces(pattern); err != nil {
		return false, err
	}

	for _, p := range expandBraces(pattern) {
		re, err := globToRegexp(p)
		if err != nil {
			return false, fmt.Errorf("glob error: %w", err)
		}
		if re.MatchString(input) {
			return true, nil
		}
	}

	return false, nil
}

// Validate checks whether a pattern is syntactically valid. It returns
// a descriptive error if the pattern has unmatched braces or invalid
// glob syntax.
func Validate(pattern string) error {
	if err := checkBraces(pattern); err != nil {
		return err
	}

	for _, p := range expandBraces(pattern) {
		if _, err := globToRegexp(p); err != nil {
			return fmt.Errorf("invalid glob syntax in %q: %w", p, err)
		}
	}

	return nil
}

// globToRegexp translates a glob pattern into an anchored regexp.
// Supported syntax: * (any characters), ? (single character),
// [...] and [!...] character classes, and \ escapes.
//
// A trailing " *" also matches the bare prefix with no arguments:
// "git status *" matches both "git status" and "git status -sb".
// This reflects the near-universal intent of command patterns; use
// "cmd ?*" to require at least one argument character.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	// Trailing " *" (unescaped) becomes an optional argument group.
	var optionalArgs bool
	if strings.HasSuffix(pattern, " *") && !strings.HasSuffix(pattern, "\\ *") {
		pattern = strings.TrimSuffix(pattern, " *")
		optionalArgs = true
	}

	var sb strings.Builder
	sb.WriteString("^")

	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '\\':
			if i+1 >= len(pattern) {
				return nil, errors.New("trailing backslash in pattern")
			}
			i++
			sb.WriteString(regexp.QuoteMeta(string(pattern[i])))
		case '*':
			sb.WriteString(".*")
		case '?':
			sb.WriteString(".")
		case '[':
			class, end, err := parseCharClass(pattern, i)
			if err != nil {
				return nil, err
			}
			sb.WriteString(class)
			i = end
		default:
			sb.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}

	if optionalArgs {
		sb.WriteString("( .*)?")
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// parseCharClass parses a [...] character class starting at index
// start (which must point at '['). It returns the regexp form of the
// class and the index of the closing ']'.
func parseCharClass(pattern string, start int) (string, int, error) {
	j := start + 1
	negated := false
	if j < len(pattern) && (pattern[j] == '^' || pattern[j] == '!') {
		negated = true
		j++
	}
	// A ']' immediately after the opening (or negation) is a literal.
	if j < len(pattern) && pattern[j] == ']' {
		j++
	}
	for j < len(pattern) && pattern[j] != ']' {
		if pattern[j] == '\\' {
			j++
		}
		j++
	}
	if j >= len(pattern) {
		return "", 0, errors.New("unmatched '[' in pattern")
	}

	inner := pattern[start+1 : j]
	if negated {
		// Strip the original negation marker and use regexp's '^'.
		inner = "^" + inner[1:]
	}
	return "[" + inner + "]", j, nil
}

// expandBraces recursively expands brace expressions in a pattern.
// For example, "{a,b}" becomes ["a", "b"] and "x{a,{b,c}}y" becomes
// ["xay", "xby", "xcy"]. Patterns without braces are returned as-is.
// Escaped braces (\{ and \}) are preserved literally.
func expandBraces(pattern string) []string {
	// Find the first top-level unescaped '{'.
	start := findUnescapedBrace(pattern, '{')
	if start == -1 {
		return []string{pattern}
	}

	// Find the matching '}'.
	end := findMatchingClose(pattern, start)
	if end == -1 {
		// Unmatched brace — treat literally (Validate catches this).
		return []string{pattern}
	}

	prefix := pattern[:start]
	suffix := pattern[end+1:]
	alternatives := splitTopLevel(pattern[start+1 : end])

	var results []string
	for _, alt := range alternatives {
		// Recurse to handle nested braces in suffix and alternatives.
		for _, expanded := range expandBraces(prefix + alt + suffix) {
			results = append(results, expanded)
		}
	}

	return results
}

// findUnescapedBrace returns the index of the first top-level
// unescaped occurrence of ch in s, or -1 if not found.
func findUnescapedBrace(s string, ch byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // Skip escaped character.
			continue
		}
		if s[i] == ch {
			return i
		}
	}
	return -1
}

// findMatchingClose returns the index of the '}' that matches the '{'
// at position start, respecting nesting and escapes.
func findMatchingClose(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // Skip escaped character.
			continue
		}
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTopLevel splits s on commas that are not inside nested braces,
// respecting escapes.
func splitTopLevel(s string) []string {
	var parts []string
	depth := 0
	var current strings.Builder

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if ch == '\\' && i+1 < len(s) {
			current.WriteByte(ch)
			i++
			current.WriteByte(s[i])
			continue
		}

		switch ch {
		case '{':
			depth++
			current.WriteByte(ch)
		case '}':
			depth--
			current.WriteByte(ch)
		case ',':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
			} else {
				current.WriteByte(ch)
			}
		default:
			current.WriteByte(ch)
		}
	}

	parts = append(parts, current.String())

	return parts
}

// checkBraces verifies that all braces in pattern are properly matched,
// respecting escapes.
func checkBraces(pattern string) error {
	depth := 0
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' {
			i++ // Skip escaped character.
			continue
		}
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return errors.New("unmatched '}' in pattern")
			}
		}
	}
	if depth > 0 {
		return errors.New("unmatched '{' in pattern")
	}
	return nil
}

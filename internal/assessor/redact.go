package assessor

import (
	"regexp"
	"strings"
	"unicode"
)

const redacted = "[REDACTED]"

type redaction struct {
	re   *regexp.Regexp
	repl string
}

// Order matters: specific token shapes run before the generic long-run
// pattern so their prefixes stay readable.
var redactions = []redaction{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----(?:.*?-----END [A-Z ]*PRIVATE KEY-----|.*)`), redacted},
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), redacted},
	{regexp.MustCompile(`\b(?:ghp|gho|ghs|ghu|ghr)_[A-Za-z0-9]{16,}`), redacted},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{16,}`), redacted},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), redacted},
	{regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9-]{10,}`), redacted},
	{regexp.MustCompile(`\b(Bearer|Api-Key) [A-Za-z0-9._-]{20,}`), "$1 " + redacted},
	{regexp.MustCompile(`(?i)("[A-Za-z0-9_-]*(?:password|passwd|token|secret|api_?key)[A-Za-z0-9_-]*"\s*:\s*)"[^"]*"`), `$1"` + redacted + `"`},
	{regexp.MustCompile(`(?i)\b([A-Za-z0-9_-]*(?:password|passwd|token|secret|api_?key)[A-Za-z0-9_-]*=)[^&\s'"]+`), "${1}" + redacted},
}

var longRunRe = regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)

const (
	maxPathComponent = 24
	minPathSlashes   = 3
)

// Redact replaces likely secrets in s with [REDACTED]. It is best-effort:
// it keeps obvious secrets off the wire but is not a guarantee.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return longRunRe.ReplaceAllStringFunc(s, func(m string) string {
		if pathLike(m) {
			return m
		}
		return redacted
	})
}

// pathLike reports whether a long run looks like a slash-separated path
// (e.g. a Go import path or temp dir) rather than an encoded secret. A
// path must be anchored (the run starts at a slash, which covers "/",
// "./", "../" and "~/" since '.' and '~' end a run) or contain several
// slashes, and every component must be short or look like a file or
// directory name. Splitting a secret in two at a single slash is
// therefore not enough to escape redaction.
func pathLike(s string) bool {
	if strings.ContainsAny(s, "+=") || !strings.Contains(s, "/") {
		return false
	}
	if !strings.HasPrefix(s, "/") && strings.Count(s, "/") < minPathSlashes {
		return false
	}
	for part := range strings.SplitSeq(s, "/") {
		if len(part) >= maxPathComponent && !nameLike(part) {
			return false
		}
	}
	return true
}

// nameLike reports whether a long path component looks like a name rather
// than random base64: it has a separator, is lowercase (like the hashed
// directories under /var/folders), or is a digit-free word with a numeric
// suffix (like the directories t.TempDir creates).
func nameLike(part string) bool {
	if strings.ContainsAny(part, "-_") {
		return true
	}
	if !strings.ContainsFunc(part, unicode.IsUpper) {
		return true
	}
	word := strings.TrimRightFunc(part, unicode.IsDigit)
	return !strings.ContainsFunc(word, unicode.IsDigit)
}

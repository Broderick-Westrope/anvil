package secrets

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// rule finds one kind of credential by its shape. group is the capture
// group that holds the secret (0 for the whole match), so context such as
// a URL's host or a PEM header stays readable. accept, when set, rejects
// matches that only look like secrets. A generic rule finds a secret by
// its context rather than a vendor's shape, so values that read as
// fixtures are skipped; a vendor-shaped fixture belongs in the allowlist.
type rule struct {
	name    string
	kind    string
	re      *regexp.Regexp
	group   int
	accept  func(match) bool
	generic bool
}

// match is one candidate secret: the value to mask, the whole match it
// sits in, and the text just before it.
type match struct {
	value  string
	full   string
	before string
}

// secretNames are the endings of a name whose value is a credential. A
// name must end in one, so max_tokens and secret_name don't count.
const secretNames = `(?:password|passwd|secret|secret[_-]?key|secret[_-]key[_-]base|secret[_-]access[_-]key|token|api[_-]?key|access[_-]?key|private[_-]?key)`

// linePrefix is what tools put before a file's line: view's "     1|",
// grep's "  Line 4: ", rg and grep -n's "path:1:", and cat -n's "     1\t".
// Line-anchored rules allow it, since that's how agents read .env files.
const linePrefix = `(?:[ \t]*\d+\||[ \t]*Line \d+(?:, Char \d+)?: |[^\s:]+:\d+[:-]|[ \t]*\d+\t)?`

// lineEnd ends a line-anchored value: spaces, an optional # comment, and
// the end of the line.
const lineEnd = `(?:[ \t]+#.*?)?[ \t]*\r?$`

// newline is a real or JSON-escaped line break.
const newline = `(?:\r?\n|\\n)`

// pemLine is one line of a PEM body: a newline, indentation, then base64
// only.
const pemLine = newline + `[ \t]*[A-Za-z0-9+/=]{16,}`

// Patterns built from parts are constants, so their concatenation happens
// at compile time.
const (
	pemBlockPattern     = `-----BEGIN [A-Z ]*PRIVATE KEY-----((?:` + pemLine + `)+(?:` + newline + `[ \t]*[A-Za-z0-9+/=]+)?)` + newline + `[ \t]*-----END [A-Z ]*PRIVATE KEY-----`
	pemTruncatedPattern = `-----BEGIN [A-Z ]*PRIVATE KEY-----((?:` + pemLine + `)+)`
	keyedQuotedPattern  = `(?i)\b[A-Za-z0-9_.-]*` + secretNames + `["']?\s*:?[:=]\s*["']([^"'\s\\]{8,})["']`
	keyedDotenvPattern  = `(?im)^` + linePrefix + `(?:export[ \t]+)?[A-Za-z0-9_.-]*` + secretNames + `=([^\s"']{8,})` + lineEnd
	dotenvKeyPattern    = `(?m)^` + linePrefix + `(?:export[ \t]+)?([A-Z0-9_]*_KEY)=([^\s"']{16,})` + lineEnd
	keyedLinePattern    = `(?im)^` + linePrefix + `[ \t]*(?:export[ \t]+)?[A-Za-z0-9_.-]*` + secretNames + `[ \t]*[:=][ \t]*([^\s"'.,(\[;]{8,})` + lineEnd
)

// tokenStart begins a vendor token at a word boundary or right after a
// literal \n, \t or \r escape, where the escape's letter would hide the
// boundary. Rules using it capture the token in group 1, so the escape
// stays.
const tokenStart = `(?:\b|\\[nrt])`

// Vendor token patterns, constants so their concatenation happens at
// compile time.
const (
	awsAccessKeyPattern     = tokenStart + `((?:AKIA|ASIA)[0-9A-Z]{16}\b)`
	githubTokenPattern      = tokenStart + `((?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{22,}))`
	anthropicKeyPattern     = tokenStart + `(sk-ant-[A-Za-z0-9_-]{20,})`
	openaiKeyPattern        = tokenStart + `(sk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,})`
	stripeKeyPattern        = tokenStart + `((?:(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{10,}|whsec_[A-Za-z0-9+/=]{20,}))`
	slackTokenPattern       = tokenStart + `(xox[abposr]-[A-Za-z0-9-]{10,})`
	gcpApiKeyPattern        = tokenStart + `(AIza[0-9A-Za-z_-]{35})`
	gcpAccessTokenPattern   = tokenStart + `(ya29\.[0-9A-Za-z_-]{20,})`
	gcpRefreshTokenPattern  = tokenStart + `(1//0[0-9A-Za-z_-]{20,})`
	npmTokenPattern         = tokenStart + `(npm_[A-Za-z0-9]{36}\b)`
	gitlabTokenPattern      = tokenStart + `(glpat-[A-Za-z0-9_-]{20,})`
	huggingfaceTokenPattern = tokenStart + `(hf_[A-Za-z0-9]{30,}\b)`
	jwtPattern              = tokenStart + `(eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`
)

// Rules run in order on the output of the one before, so vendor shapes
// name a key before the generic keyed-assignment rules see it.
var rules = []rule{
	{
		// A complete block masks its body, short last line included, and
		// leaves the BEGIN and END lines readable.
		name:  "pem",
		kind:  "private_key",
		re:    regexp.MustCompile(pemBlockPattern),
		group: 1,
	},
	{
		// A truncated block masks only whole base64 lines, so the lines
		// after it stay readable.
		name:  "pem",
		kind:  "private_key",
		re:    regexp.MustCompile(pemTruncatedPattern),
		group: 1,
	},
	{name: "aws_access_key", kind: "aws_key", re: regexp.MustCompile(awsAccessKeyPattern), group: 1},
	{name: "github_token", kind: "github_token", re: regexp.MustCompile(githubTokenPattern), group: 1},
	{name: "anthropic_key", kind: "anthropic_key", re: regexp.MustCompile(anthropicKeyPattern), group: 1},
	{
		// A key mixes cases and digits; a package slug (sk-learn-...) is
		// lower case.
		name:   "openai_key",
		kind:   "openai_key",
		re:     regexp.MustCompile(openaiKeyPattern),
		group:  1,
		accept: func(m match) bool { return classes(m.value) == 3 },
	},
	{name: "stripe_key", kind: "stripe_key", re: regexp.MustCompile(stripeKeyPattern), group: 1},
	{name: "slack_token", kind: "slack_token", re: regexp.MustCompile(slackTokenPattern), group: 1},
	{name: "gcp_api_key", kind: "gcp_api_key", re: regexp.MustCompile(gcpApiKeyPattern), group: 1},
	{name: "gcp_access_token", kind: "gcp_access_token", re: regexp.MustCompile(gcpAccessTokenPattern), group: 1},
	{name: "gcp_refresh_token", kind: "gcp_refresh_token", re: regexp.MustCompile(gcpRefreshTokenPattern), group: 1},
	{name: "npm_token", kind: "npm_token", re: regexp.MustCompile(npmTokenPattern), group: 1},
	{name: "gitlab_token", kind: "gitlab_token", re: regexp.MustCompile(gitlabTokenPattern), group: 1},
	{name: "huggingface_token", kind: "huggingface_token", re: regexp.MustCompile(huggingfaceTokenPattern), group: 1},
	{
		// A JWT signing a URL (an upload link's signature= or jwt=, or a
		// path segment) is short-lived, and masking it breaks the link.
		name:   "jwt",
		kind:   "jwt",
		re:     regexp.MustCompile(jwtPattern),
		group:  1,
		accept: func(m match) bool { return !signsURL(m.before) },
	},
	{
		// A short or single-case password of letters alone is a word,
		// which in a URL is a development or documentation one (postgres,
		// safesafe, pass), and masking it stops the agent rerunning its
		// own commands. A documented host (host, *.example.com) marks an
		// example too.
		name:    "url_password",
		kind:    "url_password",
		generic: true,
		re:      regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@"'<>{}\[\]]+:([^\s@/"'\\,;<>\[\]{}]+)@(\[[^\]]*\]|[^\s/:?#"'\\]*)`),
		group:   1,
		accept: func(m match) bool {
			v := m.value
			typed := isWord(v) && (len(v) < 16 || classes(v) < 2)
			at := strings.LastIndex(m.full, "@")
			host := strings.ToLower(strings.TrimPrefix(m.full[at:], "@"))
			return literalValue(v) && len(v) >= 8 && !typed && !documentationHost(host)
		},
	},
	{
		name:    "bearer",
		kind:    "bearer_token",
		generic: true,
		re:      regexp.MustCompile(`(?i)\b(?:Bearer|Api-Key)\s+([A-Za-z0-9._~+/=-]{20,})`),
		group:   1,
		// A token mixes classes; hyphenated words after Bearer are docs.
		accept: func(m match) bool { return classes(m.value) >= 2 || len(m.value) >= 40 },
	},
	{
		// A quoted value counts anywhere, as in JSON, YAML and source code.
		name:    "keyed_quoted",
		kind:    "keyed_secret",
		generic: true,
		re:      regexp.MustCompile(keyedQuotedPattern),
		group:   1,
		accept: func(m match) bool {
			return assignedValue(m.value) && randomLooking(m.value) && !paginationRe.MatchString(m.full)
		},
	},
	{
		// A dotenv line (NAME=value, no spaces) holds a value as written,
		// dots and brackets included, as in Django and SendGrid keys.
		// Fixture words don't count here: a .env holds real values.
		name:   "keyed_dotenv",
		kind:   "keyed_secret",
		re:     regexp.MustCompile(keyedDotenvPattern),
		group:  1,
		accept: func(m match) bool { return unquotedValue(m.value) },
	},
	{
		// A dotenv name ending in _KEY (APP_KEY, ENCRYPTION_KEY) holds a
		// credential when the value looks generated; CACHE_KEY=v2-users
		// doesn't, and public keys are meant to be shared.
		name:   "keyed_dotenv_key",
		kind:   "keyed_secret",
		re:     regexp.MustCompile(dotenvKeyPattern),
		group:  2,
		accept: func(m match) bool { return assignedValue(m.value) && randomLooking(m.value) && !publicName(m.full) },
	},
	{
		// Otherwise an unquoted value counts only when the assignment is
		// the whole line, as in INI, YAML and ~/.aws/credentials, and
		// holds no dots or brackets, since formatted code puts spaces
		// around = and : everywhere.
		name:   "keyed_line",
		kind:   "keyed_secret",
		re:     regexp.MustCompile(keyedLinePattern),
		group:  1,
		accept: func(m match) bool { return unquotedValue(m.value) },
	},
}

// literalValue rejects variable references and templates.
func literalValue(v string) bool {
	return !strings.ContainsAny(v[:1], "$%<") && !strings.HasPrefix(v, "{{")
}

// assignedValue rejects references, templates, plain numbers, publishable
// keys (pk_live_, pk_test_), which ship in client code, and identifiers
// (process.env.API_KEY, GITHUB_TOKEN_FROM_ENV), which name a value rather
// than hold one.
func assignedValue(v string) bool {
	if !literalValue(v) || strings.HasPrefix(v, "pk_") || isIdentifier(v) {
		return false
	}
	for _, r := range v {
		if !unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

var identifierRe = regexp.MustCompile(`^(?:[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)+|[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)$`)

// isIdentifier reports whether v is a dotted name or a CONSTANT_NAME. A
// segment mixing cases and digits, as in a SendGrid key's SG.x.y, is
// generated, so v isn't one.
func isIdentifier(v string) bool {
	if !identifierRe.MatchString(v) {
		return false
	}
	return !slices.ContainsFunc(strings.Split(v, "."), func(seg string) bool { return classes(seg) == 3 })
}

// unquotedValue also needs a value that looks generated (letters and
// digits, or long), so a type annotation such as `api_key: SecretStr`
// isn't masked.
func unquotedValue(v string) bool {
	if !assignedValue(v) {
		return false
	}
	if len(v) >= 20 {
		return true
	}
	var letter, digit bool
	for _, r := range v {
		letter = letter || unicode.IsLetter(r)
		digit = digit || unicode.IsDigit(r)
	}
	return letter && digit
}

// fakeWords start the words of a value written for a test or an example.
// Real secrets are random, so a word of theirs rarely starts with one.
var fakeWords = []string{"abcdefgh", "qwerty", "test", "fake", "dummy", "example", "sample", "mock", "placeholder", "changeme", "xxxx", "redacted", "your"}

// looksFake reports whether a word of v (split at anything but a letter,
// and at a lower-to-upper change) starts with a fake word, as in
// "fake-token-1" or "testAPIKey", but not "latestbuild".
func looksFake(v string) bool {
	return slices.ContainsFunc(words(v), func(w string) bool {
		return slices.ContainsFunc(fakeWords, func(fake string) bool { return strings.HasPrefix(w, fake) })
	})
}

func words(v string) []string {
	found := wordRe.FindAllString(v, -1)
	for i, w := range found {
		found[i] = strings.ToLower(w)
	}
	return found
}

// wordRe finds the words of a value: letter runs, split before a capital,
// so testAPIKey has the words test, A, P, I, Key.
var wordRe = regexp.MustCompile(`[A-Za-z][a-z]*`)

func signsURL(before string) bool {
	return strings.HasSuffix(before, "/") || strings.HasSuffix(before, "signature=") || strings.HasSuffix(before, "jwt=")
}

// paginationRe matches the names of page cursors and request IDs, which
// APIs call tokens but which the agent needs to send back.
var paginationRe = regexp.MustCompile(`(?i)(?:page|next|continuation|sync|resume|cursor|idempotency|request)[_-]?token["']?\s*[:=]`)

// isWord reports whether v is letters alone.
func isWord(v string) bool {
	return !strings.ContainsFunc(v, func(r rune) bool { return !unicode.IsLetter(r) })
}

// documentationHost reports whether host is a documentation stand-in.
func documentationHost(host string) bool {
	return host == "host" || host == "hostname" || strings.Contains(host, "example")
}

// randomLooking reports whether a value quoted in code looks generated
// rather than typed: lower case, upper case and digits; letters and digits
// with no separators; or two classes over 24 characters, as hex and base64
// keys are. Fixtures such as "secret-token-1234" are none of these.
func randomLooking(v string) bool {
	if len(v) < 12 {
		return false
	}
	n := classes(v)
	switch {
	case n == 3:
		return true
	case n == 2 && strings.ContainsFunc(v, unicode.IsDigit) && !strings.ContainsAny(v, "-_. "):
		return true
	}
	return n >= 2 && len(v) >= 24
}

// classes counts the character classes in v: lower case, upper case and
// digits.
func classes(v string) int {
	var lower, upper, digit bool
	for _, r := range v {
		lower = lower || unicode.IsLower(r)
		upper = upper || unicode.IsUpper(r)
		digit = digit || unicode.IsDigit(r)
	}
	n := 0
	for _, has := range []bool{lower, upper, digit} {
		if has {
			n++
		}
	}
	return n
}

// publicName reports whether an assignment's name marks a value meant to
// be shared, such as NEXT_PUBLIC_ or a publishable key.
func publicName(s string) bool {
	upper := strings.ToUpper(s)
	return strings.Contains(upper, "PUBLIC") || strings.Contains(upper, "PUBLISHABLE")
}

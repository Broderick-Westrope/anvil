package secrets

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

const minKnownLen = 12

var (
	envSecretSuffixes = []string{"_KEY", "_APIKEY", "_TOKEN", "_PAT", "_SECRET", "_PASSWORD", "_PASSWD", "_PASS", "_PWD", "_CREDENTIALS"}
	envSecretNames    = []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIALS"}
	booleanWords      = []string{"true", "false", "yes", "no", "on", "off"}
)

// knownForm is one way a known value can appear in text.
type knownForm struct {
	text  string
	value string
	kind  string
}

// AddKnown adds exact values to mask wherever they appear: as written,
// JSON-escaped and URL-encoded. Values that can't be secrets (short ones,
// paths, numbers, booleans, URLs) are skipped. The set only grows, so a
// rotated token stays masked wherever it is in history; adding a value
// that is already known keeps the cached results.
func (d *Detector) AddKnown(known ...Known) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var forms []knownForm
	for _, k := range known {
		k.Value = strings.TrimSpace(k.Value)
		if _, ok := d.known[k.Value]; ok || !qualifies(k.Value) {
			continue
		}
		d.known[k.Value] = struct{}{}
		kind := sanitizeKind(k.Kind)
		for _, text := range encodings(k.Value) {
			forms = append(forms, knownForm{text: text, value: k.Value, kind: kind})
		}
	}
	if len(forms) == 0 {
		return
	}
	forms = append(slices.Clone(d.forms), forms...)
	// Longest first, so a value never masks part of a longer one.
	slices.SortStableFunc(forms, func(a, b knownForm) int { return len(b.text) - len(a.text) })
	d.forms = forms
	clear(d.clean)
	clear(d.masked)
	d.maskedBytes = 0
}

// EnvKnown returns the values in environ (KEY=value pairs, as from
// os.Environ) whose names say they hold a credential and which could be
// one. Each value's kind is its lowercased name.
func EnvKnown(environ []string) []Known {
	var known []Known
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if !ok || !secretEnvName(upper) || !qualifies(value) {
			continue
		}
		// A bare _KEY name (CACHE_KEY, FEATURE_FLAG_KEY) is as often a
		// lookup key as a credential, so its value must look generated.
		if strings.HasSuffix(upper, "_KEY") && !strings.HasSuffix(upper, "API_KEY") && !randomLooking(value) {
			continue
		}
		known = append(known, Known{Kind: strings.ToLower(name), Value: value})
	}
	return known
}

// HeaderKnown returns the credentials among HTTP headers: Authorization,
// Proxy-Authorization and Cookie, and headers named like a key, token or
// secret. An auth scheme's credential (after "Bearer ", "Basic ", "Token ")
// is returned on its own too. Other headers, such as Accept, are left out.
func HeaderKnown(kind string, headers map[string]string) []Known {
	var known []Known
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		if !credentialHeader(name) {
			continue
		}
		value := headers[name]
		known = append(known, Known{Kind: kind, Value: value})
		if credential, ok := cutAuthScheme(value); ok {
			known = append(known, Known{Kind: kind, Value: credential})
		}
	}
	return known
}

func credentialHeader(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case "authorization", "proxy-authorization", "cookie":
		return true
	}
	for _, part := range []string{"key", "token", "secret"} {
		if strings.HasSuffix(name, "-"+part) || strings.HasSuffix(name, "_"+part) {
			return true
		}
	}
	return strings.Contains(name, "api-key") || strings.Contains(name, "apikey")
}

// cutAuthScheme returns the credential after an HTTP auth scheme, which
// HTTP compares without case.
func cutAuthScheme(value string) (string, bool) {
	for _, scheme := range []string{"bearer ", "basic ", "token "} {
		if len(value) > len(scheme) && strings.EqualFold(value[:len(scheme)], scheme) {
			return value[len(scheme):], true
		}
	}
	return "", false
}

// WithoutAuthScheme returns value without a leading HTTP auth scheme, as
// for a provider API key stored as "Bearer <token>".
func WithoutAuthScheme(value string) string {
	if credential, ok := cutAuthScheme(value); ok {
		return credential
	}
	return value
}

func secretEnvName(name string) bool {
	if publicName(name) {
		return false
	}
	if slices.Contains(envSecretNames, name) {
		return true
	}
	return slices.ContainsFunc(envSecretSuffixes, func(suffix string) bool {
		return strings.HasSuffix(name, suffix)
	})
}

// qualifies reports whether v could be a secret worth matching exactly.
func qualifies(v string) bool {
	if len(v) < minKnownLen || strings.Contains(v, placeholderPrefix) {
		return false
	}
	if isPath(v) || strings.Contains(v, "://") || slices.Contains(booleanWords, strings.ToLower(v)) {
		return false
	}
	return strings.ContainsFunc(v, func(r rune) bool { return !unicode.IsDigit(r) })
}

// isPath reports whether v starts like a file path. A value with a slash
// inside, such as an AWS secret key, isn't one, and nor is a generated
// value that merely starts with one (base64 can), unless it has more path
// segments or an extension.
func isPath(v string) bool {
	for _, prefix := range []string{"./", "../", "~/"} {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(v, "/"); ok {
		return strings.ContainsAny(rest, "/.") || classes(rest) < 3
	}
	return driveRe.MatchString(v)
}

var driveRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func encodings(v string) []string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Encoding a string can't fail.
	_ = enc.Encode(v)
	forms := []string{v, strings.TrimSuffix(strings.TrimSpace(buf.String()), `"`)[1:]}
	forms = append(forms, url.QueryEscape(v), url.PathEscape(v))
	slices.Sort(forms)
	return slices.Compact(forms)
}

// sanitizeKind keeps a kind to the characters a placeholder allows.
func sanitizeKind(kind string) string {
	kind = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return unicode.ToLower(r)
		}
		return '_'
	}, kind)
	if kind == "" {
		return "secret"
	}
	return kind
}

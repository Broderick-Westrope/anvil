// Package secrets finds credentials in text and replaces them with
// placeholders such as [SECRET:stripe_key:3f9a01c2d4e5f6], so they can be kept
// away from models. Masking is one way: nothing maps a placeholder back to
// its value. The tag is a keyed hash of the value, so the same value always
// gets the same placeholder under one key, and a short password's tag
// can't be reversed by lookup.
package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// GuardRefusal starts the tool error a guarded tool returns for a
// placeholder. It lives here, below the agent that writes it, so the scan
// harness in this package can count refusals.
const GuardRefusal = "Refused: the input contains"

const (
	placeholderPrefix = "[SECRET:"
	tagLen            = 8
	checkLen          = 6
	maxCleanEntries   = 50_000
	// maxMaskedBytes bounds the masked texts kept, since one tool result
	// can be hundreds of KB and every instance keeps its own cache.
	maxMaskedBytes = 8 << 20
	// beforeLen is how much text before a match a rule may look at.
	beforeLen = 16
)

var placeholderRe = regexp.MustCompile(`\[SECRET:([a-z0-9_]+):([0-9a-f]{8})([0-9a-f]{6})\]`)

// Known is an exact secret value Anvil holds, such as a provider API key.
// Kind names it in the placeholder.
type Known struct {
	Kind  string
	Value string
}

// Finding is one masked secret: what kind it was, which rule found it and
// the placeholder that replaced it.
type Finding struct {
	Kind        string
	Rule        string
	Placeholder string
}

// Detector masks secrets. It is safe for concurrent use, and a nil
// Detector (masking off) masks nothing. Results are cached by a hash of
// the text until a new known value is added, so the history a session
// resends on every request is scanned once.
type Detector struct {
	key        []byte
	allowExact map[string]struct{}
	allowRe    []*regexp.Regexp

	mu     sync.Mutex
	known  map[string]struct{}
	forms  []knownForm
	clean  map[[sha256.Size]byte]struct{}
	masked map[[sha256.Size]byte]maskResult
	// maskedBytes is the size of the texts in masked.
	maskedBytes int
	// maxClean and maxMaskedBytes bound the two caches. They are fields
	// rather than constants only so tests can shrink them.
	maxClean, maxMaskedBytes int
}

// String, GoString and LogValue keep a Detector's known values and key out
// of formatted output and logs.
func (d *Detector) String() string { return "secrets.Detector" }

// GoString implements fmt.GoStringer.
func (d *Detector) GoString() string { return d.String() }

// Format implements fmt.Formatter, so every verb prints the same.
func (d *Detector) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(d.String())) }

// LogValue implements slog.LogValuer.
func (d *Detector) LogValue() slog.Value { return slog.StringValue(d.String()) }

type maskResult struct {
	text     string
	findings []Finding
}

// New builds a detector. Each allow entry is an exact value, or a regular
// expression after a "re:" prefix, that is never masked; an entry that
// doesn't compile is skipped. key keys the placeholder tags.
func New(allow []string, key []byte) *Detector {
	d := &Detector{
		key:        key,
		allowExact: map[string]struct{}{},
		known:      map[string]struct{}{},
		clean:      map[[sha256.Size]byte]struct{}{},
		masked:     map[[sha256.Size]byte]maskResult{},

		maxClean:       maxCleanEntries,
		maxMaskedBytes: maxMaskedBytes,
	}
	for _, entry := range allow {
		re, isRe, err := allowRegexp(entry)
		switch {
		case !isRe:
			d.allowExact[entry] = struct{}{}
		case err != nil:
			// CheckAllow rejects these at config load.
			slog.Warn("Skipping secrets allow entry that is not a valid regexp", "error", err)
		default:
			d.allowRe = append(d.allowRe, re)
		}
	}
	return d
}

// CheckAllow reports the first allow entry whose regular expression doesn't
// compile, so config can reject it rather than New skipping it.
func CheckAllow(allow []string) error {
	for i, entry := range allow {
		_, _, err := allowRegexp(entry)
		if err != nil {
			return fmt.Errorf("allow[%d]: %w", i, err)
		}
	}
	return nil
}

// allowRegexp compiles an allow entry's regular expression, anchored so it
// must match the whole value, and reports whether the entry is one.
func allowRegexp(entry string) (*regexp.Regexp, bool, error) {
	pattern, isRe := strings.CutPrefix(entry, "re:")
	if !isRe {
		return nil, false, nil
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	return re, true, err
}

// Mask returns s with each secret replaced by its placeholder, and what it
// found. Text that is already a placeholder is never masked again.
func (d *Detector) Mask(s string) (string, []Finding) {
	if d == nil || s == "" {
		return s, nil
	}
	return d.maskText(s, true)
}

// maskText is Mask, logging what it finds only when log is set, so a
// string masked inside MaskJSON isn't logged twice.
func (d *Detector) maskText(s string, log bool) (string, []Finding) {
	return d.cached(textDomain, s, log, func(forms []knownForm) (string, []Finding) { return d.mask(s, forms) })
}

// Cache domains keep the results of different computations over the same
// text apart.
const (
	textDomain byte = iota
	jsonDomain
)

func cacheKey(domain byte, s string) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte{domain})
	h.Write([]byte(s))
	var key [sha256.Size]byte
	h.Sum(key[:0])
	return key
}

// cached returns the result of compute for s, computing it at most once
// per set of known values. When a value is added while compute runs, it
// runs again, so a result is never masked with an outdated set.
func (d *Detector) cached(domain byte, s string, log bool, compute func([]knownForm) (string, []Finding)) (string, []Finding) {
	hash := cacheKey(domain, s)
	d.mu.Lock()
	for {
		if _, ok := d.clean[hash]; ok {
			d.mu.Unlock()
			return s, nil
		}
		if res, ok := d.masked[hash]; ok {
			d.mu.Unlock()
			return res.text, slices.Clone(res.findings)
		}
		forms := d.forms
		d.mu.Unlock()

		text, findings := compute(forms)

		d.mu.Lock()
		// The set only grows, so a longer one means a value was added.
		if len(d.forms) != len(forms) {
			continue
		}
		d.store(hash, text, findings)
		d.mu.Unlock()
		if len(findings) == 0 {
			return s, nil
		}
		if log {
			logFindings(findings)
		}
		return text, slices.Clone(findings)
	}
}

// logFindings logs what was masked by kind and rule, never the
// placeholders, so logs say nothing about values.
func logFindings(findings []Finding) {
	rules := make([]string, len(findings))
	for i, f := range findings {
		rules[i] = f.Kind + "/" + f.Rule
	}
	slog.Debug("Masked secrets", "findings", rules)
}

// store caches a result, restarting a cache that is full. A result bigger
// than the whole bound isn't kept. It must be called with d.mu held.
func (d *Detector) store(hash [sha256.Size]byte, text string, findings []Finding) {
	if len(findings) == 0 {
		if len(d.clean) >= d.maxClean {
			clear(d.clean)
		}
		d.clean[hash] = struct{}{}
		return
	}
	if _, ok := d.masked[hash]; ok || len(text) > d.maxMaskedBytes {
		return
	}
	if d.maskedBytes+len(text) > d.maxMaskedBytes {
		clear(d.masked)
		d.maskedBytes = 0
	}
	d.masked[hash] = maskResult{text: text, findings: findings}
	d.maskedBytes += len(text)
}

// Issued reports the placeholders in s that a detector with this key
// produced. Each placeholder carries a check over its kind and tag, so this
// holds across instances, restarts and summaries, and placeholder-shaped
// text a web page planted or a doc quotes isn't reported.
func (d *Detector) Issued(s string) []string {
	if d == nil || !strings.Contains(s, placeholderPrefix) {
		return nil
	}
	var found []string
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		if hmac.Equal([]byte(m[3]), []byte(d.check(m[1], m[2]))) && !slices.Contains(found, m[0]) {
			found = append(found, m[0])
		}
	}
	return found
}

func (d *Detector) mask(s string, forms []knownForm) (string, []Finding) {
	var findings []Finding
	for _, f := range forms {
		if !strings.Contains(s, f.text) || d.allowed(f.value) {
			continue
		}
		p := d.placeholder(f.kind, f.value)
		s = strings.ReplaceAll(s, f.text, p)
		findings = append(findings, Finding{Kind: f.kind, Rule: "known", Placeholder: p})
	}
	for _, r := range rules {
		var found []Finding
		s, found = d.applyRule(s, r)
		findings = append(findings, found...)
	}
	return s, findings
}

// applyRule masks the rule's capture group (or the whole match) in every
// match the rule accepts.
func (d *Detector) applyRule(s string, r rule) (string, []Finding) {
	matches := r.re.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	var b strings.Builder
	var findings []Finding
	last := 0
	for _, loc := range matches {
		start, end := loc[2*r.group], loc[2*r.group+1]
		m := match{value: s[start:end], full: s[loc[0]:loc[1]], before: s[max(0, start-beforeLen):start]}
		if strings.Contains(m.full, placeholderPrefix) || (r.generic && looksFake(m.value)) || (r.accept != nil && !r.accept(m)) || d.allowed(m.value) {
			continue
		}
		p := d.placeholder(r.kind, m.value)
		b.WriteString(s[last:start])
		b.WriteString(p)
		last = end
		findings = append(findings, Finding{Kind: r.kind, Rule: r.name, Placeholder: p})
	}
	if len(findings) == 0 {
		return s, nil
	}
	b.WriteString(s[last:])
	return b.String(), findings
}

func (d *Detector) allowed(value string) bool {
	if _, ok := d.allowExact[value]; ok {
		return true
	}
	for _, re := range d.allowRe {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

// placeholder names value: a tag that is the same for the same value,
// and a check that lets Issued tell it from a forgery.
func (d *Detector) placeholder(kind, value string) string {
	tag := d.mac(value)[:tagLen]
	return placeholderPrefix + kind + ":" + tag + d.check(kind, tag) + "]"
}

func (d *Detector) check(kind, tag string) string {
	return d.mac("placeholder|" + kind + "|" + tag)[:checkLen]
}

func (d *Detector) mac(s string) string {
	mac := hmac.New(sha256.New, d.key)
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))
}

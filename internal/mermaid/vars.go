package mermaid

import (
	"errors"
	"regexp"
	"strings"
)

// Theme maps Pele CSS variables (without the leading "--") to values, e.g.
// "pele-bg" -> "#1a1b26".
type Theme map[string]string

var errVarsTooLarge = errors.New("css variables expand too large")

// minVarsLimit is the least output resolveVars allows, so small diagrams
// can use theme values longer than the var() they replace.
const minVarsLimit = 1 << 20

// rootStyle matches the root <svg> element's style attribute.
var rootStyle = regexp.MustCompile(`^<svg(?:\s+[^\s=>]+="[^"]*")*?\s+style="([^"]*)"`)

// resolveVars substitutes every CSS var() in svg's attribute values, because
// resvg cannot resolve custom properties, then applies each of post to every
// attribute value. Pele declares its variables only in the root <svg>
// element's style attribute and emits no <style> element, so only those
// declarations count. Text is never touched: labels are model-written and
// may contain anything that looks like CSS. Theme entries take precedence
// over the SVG's declarations. The output is capped at a few times the
// input, so chained variables can't expand without bound.
func resolveVars(svg string, theme Theme, post ...func(string) string) (string, error) {
	r := &varResolver{
		defs:      map[string]string{},
		resolved:  map[string]string{},
		resolving: map[string]bool{},
		limit:     max(4*len(svg), minVarsLimit),
	}
	if m := rootStyle.FindStringSubmatch(svg); m != nil {
		for _, decl := range splitTopLevel(m[1], ';') {
			name, value, ok := strings.Cut(decl, ":")
			name = strings.TrimSpace(name)
			if ok && strings.HasPrefix(name, "--") {
				r.defs[name[2:]] = strings.TrimSpace(value)
			}
		}
	}
	for name, value := range theme {
		r.defs[name] = value
	}

	var out strings.Builder
	inTag := false
	for svg != "" {
		if !inTag {
			i := strings.IndexByte(svg, '<')
			if i < 0 {
				out.WriteString(svg)
				break
			}
			out.WriteString(svg[:i+1])
			svg, inTag = svg[i+1:], true
			continue
		}
		i := strings.IndexAny(svg, `>"'`)
		if i < 0 {
			out.WriteString(svg)
			break
		}
		out.WriteString(svg[:i+1])
		quote := svg[i]
		svg = svg[i+1:]
		if quote == '>' {
			inTag = false
			continue
		}
		end := strings.IndexByte(svg, quote)
		if end < 0 {
			out.WriteString(svg)
			break
		}
		value, err := r.substitute(svg[:end])
		if err != nil {
			return "", err
		}
		for _, f := range post {
			value = f(value)
		}
		out.WriteString(value)
		out.WriteByte(quote)
		if out.Len() > r.limit {
			return "", errVarsTooLarge
		}
		svg = svg[end+1:]
	}
	return out.String(), nil
}

// splitTopLevel splits s at each sep outside parentheses.
func splitTopLevel(s string, sep rune) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// varResolver expands var() against a set of declarations, resolving each
// declaration once. A declaration that refers back to itself, directly or
// not, falls back as if it were undeclared.
type varResolver struct {
	defs      map[string]string
	resolved  map[string]string
	resolving map[string]bool
	limit     int
}

func (r *varResolver) substitute(s string) (string, error) {
	var out strings.Builder
	for {
		idx := strings.Index(s, "var(")
		if idx < 0 {
			out.WriteString(s)
			break
		}
		out.WriteString(s[:idx])
		body, rest, ok := matchParen(s[idx+len("var("):])
		if !ok {
			out.WriteString(s[idx:])
			break
		}
		name, fallback, _ := strings.Cut(body, ",")
		value, err := r.lookup(strings.TrimPrefix(strings.TrimSpace(name), "--"), strings.TrimSpace(fallback))
		if err != nil {
			return "", err
		}
		out.WriteString(value)
		if out.Len() > r.limit {
			return "", errVarsTooLarge
		}
		s = rest
	}
	return out.String(), nil
}

func (r *varResolver) lookup(name, fallback string) (string, error) {
	if value, ok := r.resolved[name]; ok {
		return value, nil
	}
	raw, ok := r.defs[name]
	if !ok || r.resolving[name] {
		return r.substitute(fallback)
	}
	r.resolving[name] = true
	value, err := r.substitute(raw)
	delete(r.resolving, name)
	if err != nil {
		return "", err
	}
	r.resolved[name] = value
	return value, nil
}

// matchParen splits s at the ')' closing an already-opened paren.
func matchParen(s string) (body, rest string, ok bool) {
	depth := 1
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[:i], s[i+1:], true
			}
		}
	}
	return "", "", false
}

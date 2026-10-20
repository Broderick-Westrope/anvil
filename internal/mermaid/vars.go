package mermaid

import (
	"regexp"
	"strings"
)

// Theme maps Pele CSS variables (without the leading "--") to values, e.g.
// "pele-bg" -> "#1a1b26".
type Theme map[string]string

// TokyoNight is Anvil's default palette expressed as Pele variables.
var TokyoNight = Theme{
	"pele-bg":           "#1a1b26",
	"pele-fg":           "#c0caf5",
	"pele-muted":        "#565f89",
	"pele-line":         "#a9b1d6",
	"pele-surface":      "#292e42",
	"pele-surface-alt":  "#24283b",
	"pele-border":       "#bb9af7",
	"pele-accent":       "#7aa2f7",
	"pele-critical":     "#f7768e",
	"pele-error":        "#db4b4b",
	"pele-series-1":     "#7aa2f7",
	"pele-series-2":     "#ff9e64",
	"pele-series-3":     "#9ece6a",
	"pele-series-4":     "#f7768e",
	"pele-series-5":     "#73daca",
	"pele-series-6":     "#e0af68",
	"pele-series-7":     "#bb9af7",
	"pele-series-8":     "#2ac3de",
	"pele-font":         "sans-serif",
	"pele-font-mono":    "monospace",
	"pele-title-weight": "bold",
}

var customPropertyDecl = regexp.MustCompile(`--([A-Za-z0-9_-]+)\s*:\s*`)

// ResolveVars substitutes every CSS var() in svg, because resvg cannot
// resolve custom properties. Declarations are treated as global, which holds
// for Pele's output where they only appear on the root element. Theme
// entries take precedence over declarations in the SVG.
func ResolveVars(svg string, theme Theme) string {
	defs := map[string]string{}
	for _, loc := range customPropertyDecl.FindAllStringSubmatchIndex(svg, -1) {
		name := svg[loc[2]:loc[3]]
		defs[name] = readValue(svg[loc[1]:])
	}
	for name, value := range theme {
		defs[name] = value
	}
	return substituteVars(svg, defs, 0)
}

// readValue returns a declaration value up to the next top-level ';' or the
// end of the attribute.
func readValue(s string) string {
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ';', '"':
			if depth == 0 {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return strings.TrimSpace(s)
}

const maxVarDepth = 8

func substituteVars(s string, defs map[string]string, depth int) string {
	if depth > maxVarDepth || !strings.Contains(s, "var(") {
		return s
	}
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
		name = strings.TrimPrefix(strings.TrimSpace(name), "--")
		value, found := defs[name]
		if !found {
			value = strings.TrimSpace(fallback)
		}
		out.WriteString(substituteVars(value, defs, depth+1))
		s = rest
	}
	return out.String()
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

// With returns a copy of t with name set to value.
func (t Theme) With(name, value string) Theme {
	out := make(Theme, len(t)+1)
	for k, v := range t {
		out[k] = v
	}
	out[name] = value
	return out
}

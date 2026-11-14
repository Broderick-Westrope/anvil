package secrets

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// MaskJSON masks JSON (a tool call's input, a JSON tool result) so it stays
// valid JSON, and returns what it found. Each string is masked decoded, where
// multi-line rules see real newlines, and the re-encoded input is masked
// again with the rules alone, where a key gives a value its context. Rules
// only replace text that starts and ends inside a JSON string (a PEM body
// takes whole \n escapes), and placeholders hold no quotes or backslashes,
// so the second pass keeps the encoding valid. When anything is
// masked the input is re-encoded, so its keys come out sorted. Input that
// isn't JSON is masked as text. Results are cached like Mask's, since
// history repeats on every request.
func (d *Detector) MaskJSON(input string) (string, []Finding) {
	if d == nil || input == "" {
		return input, nil
	}
	// maskJSON ignores the forms snapshot: its Mask calls take their own,
	// and cached runs it again if a value is added meanwhile.
	return d.cached(jsonDomain, input, true, func([]knownForm) (string, []Finding) { return d.maskJSON(input) })
}

// IsJSON reports whether text is a JSON object or array, as MCP tools,
// curl and gh api print.
func IsJSON(text string) bool {
	trimmed := strings.TrimSpace(text)
	return (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(text))
}

func (d *Detector) maskJSON(input string) (string, []Finding) {
	if !json.Valid([]byte(input)) {
		return d.maskText(input, false)
	}
	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	var value any
	err := dec.Decode(&value)
	if err != nil {
		return d.maskText(input, false)
	}
	value, findings := d.maskValue(value)
	encoded := input
	if len(findings) > 0 {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		err := enc.Encode(value)
		if err != nil {
			return input, nil
		}
		encoded = strings.TrimSuffix(buf.String(), "\n")
	}
	// Known values were masked decoded; matching them again in the encoded
	// text could start inside an escape and leave a stray backslash.
	text, more := d.mask(encoded, nil)
	return text, slices.Concat(findings, more)
}

func (d *Detector) maskValue(value any) (any, []Finding) {
	var findings []Finding
	switch v := value.(type) {
	case string:
		return d.maskText(v, false)
	case []any:
		for i := range v {
			var f []Finding
			v[i], f = d.maskValue(v[i])
			findings = append(findings, f...)
		}
	case map[string]any:
		masked := make(map[string]any, len(v))
		for _, k := range slices.Sorted(maps.Keys(v)) {
			// Two keys masking to one placeholder would need the same
			// secret twice, so a key can't overwrite another value.
			key, kf := d.maskText(k, false)
			val, vf := d.maskValue(v[k])
			masked[key] = val
			findings = append(findings, kf...)
			findings = append(findings, vf...)
		}
		return masked, findings
	}
	return value, findings
}

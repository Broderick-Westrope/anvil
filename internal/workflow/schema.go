package workflow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Schema is a compiled JSON Schema for structured output.
type Schema struct {
	raw      json.RawMessage
	resolved *jsonschema.Resolved
}

// CompileSchema compiles a JSON Schema written as a plain JS object.
func CompileSchema(raw json.RawMessage) (*Schema, error) {
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve schema: %w", err)
	}
	return &Schema{raw: raw, resolved: resolved}, nil
}

// Raw is the schema as written.
func (s *Schema) Raw() json.RawMessage { return s.raw }

// Validate checks a JSON document against the schema.
func (s *Schema) Validate(doc json.RawMessage) error {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return s.resolved.Validate(v)
}

var jsonBlock = regexp.MustCompile("(?s)```json\\s*\\n(.*?)```")

// lastJSONBlock extracts the final ```json fenced block from a reply, or
// the whole reply when it has none.
func lastJSONBlock(reply string) json.RawMessage {
	matches := jsonBlock.FindAllStringSubmatch(reply, -1)
	if len(matches) == 0 {
		return json.RawMessage(strings.TrimSpace(reply))
	}
	return json.RawMessage(strings.TrimSpace(matches[len(matches)-1][1]))
}

func schemaInstructions(s *Schema) string {
	return "\n\nEnd your reply with a single ```json fenced block whose value matches this JSON Schema:\n```json\n" +
		string(s.raw) + "\n```"
}

func schemaCorrection(reason string) string {
	return "Your last reply didn't end with a ```json block matching the schema (" + reason +
		"). Reply again, ending with only the corrected ```json block."
}

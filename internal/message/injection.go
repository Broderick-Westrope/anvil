package message

import (
	"fmt"
	"regexp"
	"strings"
)

// InjectionSource says who injected a message into a running subagent.
type InjectionSource string

const (
	InjectionSourceHuman        InjectionSource = "human"
	InjectionSourceOrchestrator InjectionSource = "orchestrator"
	InjectionSourceHarness      InjectionSource = "harness"
)

// InjectionKind says why a message was injected.
type InjectionKind string

const (
	// InjectionKindSteer is a human message delivered at a step boundary.
	InjectionKindSteer InjectionKind = "steer"
	// InjectionKindInterruptSteer is a human message that cancelled the
	// current attempt.
	InjectionKindInterruptSteer InjectionKind = "interrupt_steer"
	// InjectionKindResume is the orchestrator's prompt when it resumes a
	// subagent.
	InjectionKindResume InjectionKind = "resume"
	// InjectionKindRetry is the harness's continue message after a
	// transient failure.
	InjectionKindRetry InjectionKind = "retry"
	// InjectionKindContinue is the harness's continue message after an
	// interrupt whose steer was already delivered.
	InjectionKindContinue InjectionKind = "continue"
)

// InjectionContent is the audit record of one injected message. It lives
// next to the rendered text in a subagent_injection message, so it survives
// even when compaction summarises the text away from the model's context.
type InjectionContent struct {
	Source InjectionSource `json:"source"`
	Kind   InjectionKind   `json:"kind"`
	Text   string          `json:"text"`
	// CallID identifies the task call the message was injected in.
	CallID string `json:"call_id"`
	// Attempt numbers harness retries from 1.
	Attempt int `json:"attempt,omitempty"`
	// MaxAttempts is the retry budget Attempt counts against.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// Reason names the failure a harness retry recovers from.
	Reason string `json:"reason,omitempty"`
}

func (InjectionContent) isPart() {}

// RenderInjections returns the text the model sees for injections: each
// wrapped in an injected_message tag naming its source.
func RenderInjections(injections []InjectionContent) string {
	var rendered []string
	for _, inj := range injections {
		rendered = append(rendered, fmt.Sprintf("<injected_message source=%q>%s</injected_message>", inj.Source, EscapeInjectionTags(inj.Text)))
	}
	return strings.Join(rendered, "\n\n")
}

// injectionTag matches the start of an injected_message tag in any case.
var injectionTag = regexp.MustCompile(`(?i)<(\s*/?\s*injected_message)`)

// EscapeInjectionTags neutralises injected_message tags in text a subagent
// will read, so the text can't open or close one and claim a source.
func EscapeInjectionTags(text string) string {
	return injectionTag.ReplaceAllString(text, "&lt;$1")
}

// InjectionParts returns the parts of a subagent_injection message: the
// rendered text the model sees, then one audit record per injection.
func InjectionParts(injections []InjectionContent) []ContentPart {
	parts := []ContentPart{TextContent{Text: RenderInjections(injections)}}
	for _, inj := range injections {
		parts = append(parts, inj)
	}
	return parts
}

// Injections returns the injection records the message carries.
func (m *Message) Injections() []InjectionContent {
	var injections []InjectionContent
	for _, part := range m.Parts {
		if inj, ok := part.(InjectionContent); ok {
			injections = append(injections, inj)
		}
	}
	return injections
}

package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/charmbracelet/x/ansi"
	"github.com/dustin/go-humanize"
)

const (
	// pickerPageSize is the number of messages fetched per transcript page.
	pickerPageSize = 50
	// previewMaxLinesPerMessage caps the wrapped text lines rendered for a
	// single message before a truncation marker is appended.
	previewMaxLinesPerMessage = 12
	// previewNoMessages is the placeholder shown for empty sessions.
	previewNoMessages = "no messages"
	// previewTruncationMarker is appended when a message's text is cut.
	previewTruncationMarker = "… (truncated)"
)

// renderTail renders a transcript tail as plain (unstyled) lines. Metadata
// messages are filtered out; tool payloads, binary data, and reasoning are
// never rendered. Empty input yields the "no messages" placeholder. Pure:
// no I/O, no model state.
func renderTail(msgs []message.Message, width int) []string {
	width = max(width, 10)
	var out []string
	for _, msg := range msgs {
		filtered := message.FilterMetadataMessage(msg)
		if filtered == nil {
			continue
		}

		var lines []string
		textCount := 0
		truncated := false
		for _, part := range filtered.Parts {
			switch p := part.(type) {
			case message.TextContent:
				if strings.TrimSpace(p.Text) == "" {
					continue
				}
				if textCount >= previewMaxLinesPerMessage {
					truncated = true
					continue
				}
				wrapped := strings.Split(ansi.Wordwrap(p.Text, width, ""), "\n")
				if avail := previewMaxLinesPerMessage - textCount; len(wrapped) > avail {
					wrapped = wrapped[:avail]
					truncated = true
				}
				lines = append(lines, wrapped...)
				textCount += len(wrapped)
			case message.ToolCall:
				lines = append(lines, "→ tool: "+p.Name)
			case message.ToolResult:
				name := p.Name
				if name == "" {
					name = p.ToolCallID
				}
				lines = append(lines, fmt.Sprintf("← result: %s (%d bytes)", name, len(p.Content)))
			case message.BinaryContent:
				lines = append(lines, "[binary: "+p.MIMEType+"]")
			case message.ImageURLContent:
				lines = append(lines, "[image]")
			}
		}
		if truncated {
			lines = append(lines, previewTruncationMarker)
		}

		// A message with no renderable parts renders no role header.
		if len(lines) == 0 {
			continue
		}
		out = append(out, string(filtered.Role))
		out = append(out, lines...)
		out = append(out, "")
	}
	if len(out) == 0 {
		return []string{previewNoMessages}
	}
	// Drop the trailing blank separator.
	return out[:len(out)-1]
}

// renderPreviewHeader renders the metadata header lines for a session:
// working dir, age, message count, and pin note. Pure.
func renderPreviewHeader(sess session.Session, width int) []string {
	width = max(width, 10)
	lines := []string{
		ansi.Truncate("dir:  "+abbreviateHome(sess.WorkingDir), width, "…"),
		ansi.Truncate("age:  "+humanize.Time(time.Unix(sess.UpdatedAt, 0)), width, "…"),
		ansi.Truncate(fmt.Sprintf("msgs: %d", sess.MessageCount), width, "…"),
	}
	if sess.PinNote != "" {
		note := strings.ReplaceAll(sess.PinNote, "\n", " ")
		lines = append(lines, ansi.Truncate("note: "+note, width, "…"))
	}
	return lines
}

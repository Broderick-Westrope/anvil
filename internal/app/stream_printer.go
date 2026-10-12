package app

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/message"
)

// streamPrinter writes a session's assistant replies to output as they
// stream in, printing only the text each update adds.
type streamPrinter struct {
	output    io.Writer
	sessionID string
	onReply   func()
	readBytes map[string]int // Bytes of each message already handled.
	printed   bool
}

// print writes the text msg adds since its last update. Messages that
// aren't a reply in the printer's session are ignored.
func (p *streamPrinter) print(msg message.Message) error {
	if msg.SessionID != p.sessionID || msg.Role != message.Assistant || len(msg.Parts) == 0 {
		return nil
	}
	p.onReply()

	content := msg.Content().String()
	readBytes := p.readBytes[msg.ID]

	if len(content) < readBytes {
		slog.Error("Non-interactive: message content is shorter than read bytes", "message_length", len(content), "read_bytes", readBytes)
		return fmt.Errorf("message content is shorter than read bytes: %d < %d", len(content), readBytes)
	}

	part := content[readBytes:]
	// Trim leading whitespace. Sometimes the LLM includes leading
	// formatting and intentation, which we don't want here.
	if readBytes == 0 {
		part = strings.TrimLeft(part, " \t")
	}
	// Ignore initial whitespace-only messages.
	if p.printed || strings.TrimSpace(part) != "" {
		p.printed = true
		fmt.Fprint(p.output, part)
	}
	p.readBytes[msg.ID] = len(content)
	return nil
}

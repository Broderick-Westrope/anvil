package cacheusage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"slices"
	"strconv"
	"strings"

	"charm.land/fantasy"
)

// hashHexLen is the length of every stored hash. 64 bits is plenty to
// tell consecutive requests apart.
const hashHexLen = 16

// Fingerprint summarises a request with truncated hashes. It never holds
// prompt content.
type Fingerprint struct {
	ToolsHash, SystemHash, HistoryHash   string
	ToolCount, SystemCount, MessageCount int
	// prefix[i] is the rolling history hash after history entry i (see
	// writeHistory).
	prefix []string
	Err    string
}

type toolCanonical struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Required    []string       `json:"required"`
}

// Compute fingerprints the tools and messages Anvil passes to fantasy for
// one step.
func Compute(tools []fantasy.AgentTool, messages []fantasy.Message) Fingerprint {
	var f Fingerprint
	var errs []string

	toolsHash, err := hashTools(tools)
	if err != nil {
		errs = append(errs, fmt.Sprintf("tools: %v", err))
	}
	f.ToolsHash = toolsHash
	f.ToolCount = len(tools)

	sys := sha256.New()
	sysOK := true
	history := sha256.New()
	historyOK := true
	var sum []byte
	for i, msg := range messages {
		if msg.Role == fantasy.MessageRoleSystem {
			f.SystemCount++
			if !sysOK {
				continue
			}
			b, err := canonicalMessage(msg)
			if err != nil {
				sysOK = false
				errs = append(errs, fmt.Sprintf("system message %d: %v", i, err))
				continue
			}
			writeFramed(sys, b)
			continue
		}

		f.MessageCount++
		if !historyOK {
			continue
		}
		err := writeHistory(history, msg, func() {
			sum = history.Sum(sum[:0])
			f.prefix = append(f.prefix, truncate(sum))
		})
		if err != nil {
			historyOK = false
			errs = append(errs, fmt.Sprintf("message %d: %v", i, err))
		}
	}

	if sysOK {
		f.SystemHash = truncate(sys.Sum(nil))
	}
	if historyOK && len(f.prefix) > 0 {
		f.HistoryHash = f.prefix[len(f.prefix)-1]
	}
	if len(errs) > 0 {
		f.Err = strings.Join(errs, "; ")
	}
	return f
}

// PrefixLen is the number of history entries HistoryHash covers. Pass it
// to a later request's PrefixMatches.
func (f Fingerprint) PrefixLen() int {
	return len(f.prefix)
}

// PrefixMatches reports whether this request's first prevLen history
// entries hash to prevHistoryHash, where prevLen is the earlier request's
// PrefixLen. ok is false when prevLen is 0 or exceeds this request's
// entries, or when the prefix could not be hashed.
func (f Fingerprint) PrefixMatches(prevLen int, prevHistoryHash string) (match, ok bool) {
	if prevLen <= 0 || prevLen > len(f.prefix) || prevHistoryHash == "" {
		return false, false
	}
	return f.prefix[prevLen-1] == prevHistoryHash, true
}

// History entry kinds.
const (
	entryReasoning  = "reasoning"
	entryText       = "text"
	entryFile       = "file"
	entryToolCall   = "tool-call"
	entryToolResult = "tool-result"
	entryOther      = "other"
)

// writeHistory writes msg to h as a sequence of semantic entries and calls
// mark after each one.
//
// The same history reaches Anvil in two shapes: within a run, fantasy
// builds step messages from the streamed content; on the next run, Anvil
// rebuilds them from the database (message.Message.ToAIMessage). These
// differ in ways that do not change what the conversation says, so the
// hash ignores them:
//   - Provider options and metadata, at message and part level. Moving
//     cache markers are not a content change, and reasoning metadata does
//     not survive the database: OpenAI Responses reasoning metadata comes
//     back empty, and Anvil keeps only one reasoning block per message.
//   - Message grouping: entries carry their role but not message
//     boundaries, so tool results grouped into one message or split
//     across several hash the same.
//   - Layout within a message: all reasoning text becomes one entry, then
//     all text trimmed of surrounding whitespace, as ToAIMessage rebuilds
//     them. Empty reasoning and text are dropped.
//   - File names, which the database stores as paths.
//
// A provider-specific encoding change, such as a new reasoning signature,
// therefore does not change the hash even if it busts the provider cache.
func writeHistory(h hash.Hash, msg fantasy.Message, mark func()) error {
	role := string(msg.Role)
	var reasoning, text strings.Builder
	for _, part := range msg.Content {
		switch p := part.(type) {
		case fantasy.ReasoningPart:
			reasoning.WriteString(p.Text)
		case fantasy.TextPart:
			text.WriteString(p.Text)
		}
	}
	if reasoning.Len() > 0 {
		writeEntry(h, entryReasoning, role, reasoning.String())
		mark()
	}
	if t := strings.TrimSpace(text.String()); t != "" {
		writeEntry(h, entryText, role, t)
		mark()
	}

	for _, part := range msg.Content {
		switch p := part.(type) {
		case fantasy.ReasoningPart, fantasy.TextPart:
			continue
		case fantasy.FilePart:
			writeEntry(h, entryFile, role, p.MediaType, sampleMedia(string(p.Data)))
		case fantasy.ToolCallPart:
			writeEntry(h, entryToolCall, role, p.ToolCallID, p.ToolName, p.Input, strconv.FormatBool(p.ProviderExecuted))
		case fantasy.ToolResultPart:
			fields := append([]string{p.ToolCallID, strconv.FormatBool(p.ProviderExecuted)}, toolResultOutput(p.Output)...)
			writeEntry(h, entryToolResult, role, fields...)
		default:
			b, err := json.Marshal(part)
			if err != nil {
				return err
			}
			writeEntry(h, entryOther, role, string(b))
		}
		mark()
	}
	return nil
}

// toolResultOutput returns the output's type and content as entry fields.
func toolResultOutput(output fantasy.ToolResultOutputContent) []string {
	switch o := output.(type) {
	case fantasy.ToolResultOutputContentText:
		return []string{string(o.GetType()), o.Text}
	case fantasy.ToolResultOutputContentError:
		var msg string
		if o.Error != nil {
			msg = o.Error.Error()
		}
		return []string{string(o.GetType()), msg}
	case fantasy.ToolResultOutputContentMedia:
		return []string{string(o.GetType()), o.MediaType, o.Text, sampleMedia(o.Data)}
	case nil:
		return nil
	default:
		return []string{fmt.Sprintf("%T", o), fmt.Sprintf("%v", o)}
	}
}

// writeEntry writes one history entry as length-framed fields, so field
// and entry boundaries change the hash.
func writeEntry(h hash.Hash, kind, role string, fields ...string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(2+len(fields)))
	h.Write(n[:])
	writeFramedString(h, kind)
	writeFramedString(h, role)
	for _, field := range fields {
		writeFramedString(h, field)
	}
}

func writeFramedString(h hash.Hash, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	h.Write(n[:])
	io.WriteString(h, s)
}

// hashTools hashes tools in the order passed. fantasy may later filter,
// reorder or normalise schemas before sending
// (fantasy@v0.43.2 agent.go:1117-1143), so this hash is "as Anvil passed
// it".
func hashTools(tools []fantasy.AgentTool) (string, error) {
	h := sha256.New()
	for _, tool := range tools {
		info := tool.Info()
		b, err := json.Marshal(toolCanonical{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  info.Parameters,
			Required:    info.Required,
		})
		if err != nil {
			return "", fmt.Errorf("%s: %w", info.Name, err)
		}
		writeFramed(h, b)
	}
	return truncate(h.Sum(nil)), nil
}

// canonicalMessage marshals a system message without its message-level
// provider options. Anvil sets those only for the cache-control marker on
// the last system message (internal/agent/agent.go), so moving the marker
// must not look like a content change.
func canonicalMessage(msg fantasy.Message) ([]byte, error) {
	msg.ProviderOptions = nil
	msg.Content = sampleParts(msg.Content)
	return json.Marshal(msg)
}

// mediaSampleSize is how much of each end of a large media payload is
// hashed. Hashing whole images pushed a 500-message history with a 1 MB
// image past the 5ms budget; length plus both ends still catches a
// replaced or re-encoded image.
const mediaSampleSize = 4 << 10

// sampleParts returns parts with large file payloads replaced by
// sampleMedia. The input slice is never modified; it is copied only when a
// payload is replaced.
func sampleParts(parts []fantasy.MessagePart) []fantasy.MessagePart {
	var out []fantasy.MessagePart
	for i, part := range parts {
		p, ok := part.(fantasy.FilePart)
		if !ok || len(p.Data) <= 2*mediaSampleSize {
			continue
		}
		if out == nil {
			out = slices.Clone(parts)
		}
		p.Data = []byte(sampleMedia(string(p.Data)))
		out[i] = p
	}
	if out == nil {
		return parts
	}
	return out
}

// sampleMedia returns data, or for large payloads its length and first
// and last mediaSampleSize bytes.
func sampleMedia(data string) string {
	if len(data) <= 2*mediaSampleSize {
		return data
	}
	return strconv.Itoa(len(data)) + ":" + data[:mediaSampleSize] + data[len(data)-mediaSampleSize:]
}

// writeFramed writes b with a length prefix so block boundaries change the
// hash.
func writeFramed(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

func truncate(sum []byte) string {
	return hex.EncodeToString(sum)[:hashHexLen]
}

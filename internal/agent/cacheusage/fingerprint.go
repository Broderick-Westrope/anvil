package cacheusage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
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
	// prefix[i] is the rolling hash after non-system message i.
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
	var prev []byte
	historyOK := true
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
		b, err := canonicalMessage(msg)
		if err != nil {
			historyOK = false
			errs = append(errs, fmt.Sprintf("message %d: %v", i, err))
			continue
		}
		h := sha256.New()
		h.Write(prev)
		h.Write(b)
		prev = h.Sum(prev[:0])
		f.prefix = append(f.prefix, truncate(prev))
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

// PrefixMatches reports whether this request's first prevCount non-system
// messages hash to prevHistoryHash. ok is false when prevCount is 0 or
// exceeds MessageCount, or when the prefix could not be hashed.
func (f Fingerprint) PrefixMatches(prevCount int, prevHistoryHash string) (match, ok bool) {
	if prevCount <= 0 || prevCount > f.MessageCount || prevCount > len(f.prefix) || prevHistoryHash == "" {
		return false, false
	}
	return f.prefix[prevCount-1] == prevHistoryHash, true
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

// canonicalMessage marshals msg without its message-level provider
// options. Anvil clears those and sets them only for cache-control markers
// on the last system message and the last two messages
// (internal/agent/agent.go:443-446, 467-481), so moving markers must not
// look like a content change. Part-level options are kept: Anthropic
// reasoning signatures live there and are sent to the provider
// (fantasy@v0.43.2 providers/anthropic/anthropic.go:484-490, 1086).
func canonicalMessage(msg fantasy.Message) ([]byte, error) {
	msg.ProviderOptions = nil
	msg.Content = sampleMedia(msg.Content)
	return json.Marshal(msg)
}

// mediaSampleSize is how much of each end of a large media payload is
// hashed. Hashing whole images pushed a 500-message history with a 1 MB
// image past the 5ms budget; length plus both ends still catches a
// replaced or re-encoded image.
const mediaSampleSize = 4 << 10

// sampleMedia returns parts with large media payloads replaced by their
// length and first and last mediaSampleSize bytes. The input slice is
// never modified; it is copied only when a payload is replaced.
func sampleMedia(parts []fantasy.MessagePart) []fantasy.MessagePart {
	var out []fantasy.MessagePart
	for i, part := range parts {
		replaced, ok := samplePart(part)
		if !ok {
			continue
		}
		if out == nil {
			out = slices.Clone(parts)
		}
		out[i] = replaced
	}
	if out == nil {
		return parts
	}
	return out
}

func samplePart(part fantasy.MessagePart) (fantasy.MessagePart, bool) {
	switch p := part.(type) {
	case fantasy.FilePart:
		if len(p.Data) <= 2*mediaSampleSize {
			return nil, false
		}
		p.Data = []byte(sampleString(string(p.Data)))
		return p, true
	case fantasy.ToolResultPart:
		media, ok := p.Output.(fantasy.ToolResultOutputContentMedia)
		if !ok || len(media.Data) <= 2*mediaSampleSize {
			return nil, false
		}
		media.Data = sampleString(media.Data)
		p.Output = media
		return p, true
	}
	return nil, false
}

func sampleString(data string) string {
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

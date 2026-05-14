package anthropic

import (
	"crypto/sha256"
	"fmt"
)

const (
	// BillingSalt is a fixed salt used in billing header computation.
	BillingSalt = "59cf53e54c78"
	// CLIVersion is the pinned Claude CLI version reported in billing headers.
	CLIVersion = "2.1.112"
	// Entrypoint identifies this client as a CLI integration.
	Entrypoint = "cli"
)

// ComputeCCH returns the first 5 hex characters of the SHA-256 hash of text.
func ComputeCCH(text string) string {
	h := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", h)[:5]
}

// ComputeVersionSuffix samples characters from text at indices 4, 7, and 20
// (substituting "0" for any out-of-range index), then returns the first 3 hex
// characters of SHA-256(BillingSalt + sampled + version).
func ComputeVersionSuffix(text, version string) string {
	indices := [3]int{4, 7, 20}
	sampled := make([]byte, 0, 3)
	for _, i := range indices {
		if i < len(text) {
			sampled = append(sampled, text[i])
		} else {
			sampled = append(sampled, '0')
		}
	}
	h := sha256.Sum256([]byte(BillingSalt + string(sampled) + version))
	return fmt.Sprintf("%x", h)[:3]
}

// BuildBillingHeader assembles the full x-anthropic-billing-header value
// from the given text (typically the system prompt or first user message).
func BuildBillingHeader(text string) string {
	cch := ComputeCCH(text)
	suffix := ComputeVersionSuffix(text, CLIVersion)
	return fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=%s;",
		CLIVersion, suffix, Entrypoint, cch,
	)
}

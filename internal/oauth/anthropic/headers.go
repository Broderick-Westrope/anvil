package anthropic

// Headers returns the base OAuth header map for Anthropic API requests.
// The anthropic-beta header is intentionally omitted here; it is set in
// the provider build layer where the model ID is known. The
// Authorization header (Bearer token) is handled separately by the
// provider configuration layer.
func Headers() map[string]string {
	return map[string]string{
		"anthropic-dangerous-direct-browser-access": "true",
		"anthropic-version":                         "2023-06-01",
		"user-agent":                                "claude-cli/" + CLIVersion + " (external, cli)",
		"x-app":                                     "cli",
	}
}

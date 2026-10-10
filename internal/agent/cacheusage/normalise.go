package cacheusage

import (
	"encoding/json"
	"math"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
)

// deepSeekCacheHitField is the usage field DeepSeek uses for cache hits.
// fantasy does not map it, so it only appears in ExtraFields.
const deepSeekCacheHitField = "prompt_cache_hit_tokens"

// Tokens are per-call counts. Input never includes cached tokens.
type Tokens struct {
	Input, CacheRead, CacheWrite, Output, Reasoning int64
}

type rawUsage struct {
	Usage fantasy.Usage              `json:"usage"`
	Extra map[string]json.RawMessage `json:"extra"`
}

// Normalise corrects provider differences. providerType is the fantasy
// LanguageModel.Provider() value for the model that made the call. raw is
// JSON {"usage": <reported fantasy.Usage>, "extra": <usage ExtraFields or
// null>}.
//
// Provider() returns the provider's configured name, which defaults to the
// package Name constant (fantasy@v0.45.2 providers/anthropic/anthropic.go:188,
// providers/openai/openai.go:56, providers/google/google.go:65). Wrappers
// override it: bedrock (providers/bedrock/bedrock.go:32), azure
// (providers/azure/azure.go:51), openrouter
// (providers/openrouter/openrouter.go:32), vercel
// (providers/vercel/vercel.go:31) and openai-compat
// (providers/openaicompat/openaicompat.go:29). Anvil never calls WithName
// (internal/agent/coordinator_providers.go), so Vertex reports "google"
// because google.WithVertex only switches the backend.
func Normalise(providerType string, reported fantasy.Usage, meta fantasy.ProviderMetadata) (tokens Tokens, raw string) {
	extra := extraFields(meta)
	raw = marshalRaw(reported, extra)

	tokens = Tokens{
		Input:      reported.InputTokens,
		CacheRead:  reported.CacheReadTokens,
		CacheWrite: reported.CacheCreationTokens,
		Output:     reported.OutputTokens,
		Reasoning:  reported.ReasoningTokens,
	}

	switch providerType {
	case anthropic.Name, bedrock.Name:
		// Anthropic reports input_tokens excluding cache reads and writes
		// and fantasy copies the fields through
		// (fantasy@v0.45.2 providers/anthropic/anthropic.go:1446-1450,
		// 1731-1735). Bedrock uses the Anthropic language model.
	case openai.Name, azure.Name:
		// fantasy subtracts cached tokens from the prompt count for chat
		// completions (providers/openai/language_model_hooks.go:246, 280)
		// and the Responses API (providers/openai/responses_language_model.go:411).
		// Azure uses the same language models.
	case openrouter.Name:
		// providers/openrouter/language_model_hooks.go:459, 503.
	case vercel.Name:
		// providers/vercel/language_model_hooks.go:518, 564.
	case google.Name:
		// InputTokens is PromptTokenCount, which includes cached content
		// (fantasy@v0.45.2 providers/google/google.go:1511, 1516).
		//
		// Streaming sums CacheReadTokens across usage chunks while keeping
		// the first chunk's InputTokens (providers/google/google.go:857,
		// 1134), so CacheRead can exceed the real count when Gemini repeats
		// usage on several chunks. Cached tokens cannot exceed the prompt,
		// so CacheRead is capped at it. raw keeps the reported values.
		tokens.CacheRead = min(tokens.CacheRead, reported.InputTokens)
		tokens.Input -= tokens.CacheRead
	case openaicompat.Name:
		// openai-compat uses fantasy's default OpenAI usage functions
		// (providers/openai/language_model_hooks.go:246, 280), which only
		// read prompt_tokens_details.cached_tokens. DeepSeek reports hits in
		// prompt_cache_hit_tokens instead, left in ExtraFields
		// (providers/openai/language_model_hooks.go:252, 304). Its
		// prompt_tokens still includes the hits.
		if tokens.CacheRead == 0 {
			if hits, ok := deepSeekCacheHits(extra); ok {
				tokens.CacheRead = hits
				tokens.Input = max(tokens.Input-hits, 0)
			}
		}
	default:
		// Unknown provider types are assumed to follow OpenAI semantics,
		// where fantasy has already excluded cached tokens from input.
	}

	return tokens, raw
}

// extraFields returns the OpenAI usage ExtraFields from meta. fantasy keys
// the metadata by openai.Name even for openai-compat
// (fantasy@v0.45.2 providers/openai/language_model_hooks.go:307); the
// openai-compat key is checked as a fallback.
func extraFields(meta fantasy.ProviderMetadata) map[string]json.RawMessage {
	for _, key := range []string{openai.Name, openaicompat.Name} {
		data, ok := meta[key]
		if !ok {
			continue
		}
		if m, ok := data.(*openai.ProviderMetadata); ok && m != nil && len(m.ExtraFields) > 0 {
			return m.ExtraFields
		}
	}
	return nil
}

func deepSeekCacheHits(extra map[string]json.RawMessage) (int64, bool) {
	value, ok := extra[deepSeekCacheHitField]
	if !ok {
		return 0, false
	}
	var hits float64
	if err := json.Unmarshal(value, &hits); err != nil {
		return 0, false
	}
	// float64(math.MaxInt64) rounds up to 2^63, which int64 cannot hold.
	if hits <= 0 || hits != math.Trunc(hits) || hits >= math.MaxInt64 {
		return 0, false
	}
	return int64(hits), true
}

// marshalRaw falls back to the reported usage alone when extra holds
// invalid raw JSON.
func marshalRaw(reported fantasy.Usage, extra map[string]json.RawMessage) string {
	for _, raw := range []rawUsage{{Usage: reported, Extra: extra}, {Usage: reported}} {
		if b, err := json.Marshal(raw); err == nil {
			return string(b)
		}
	}
	return "{}"
}

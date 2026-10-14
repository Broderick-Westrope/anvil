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
// package Name constant (New in fantasy's anthropic, openai and google
// packages, as of fantasy v0.45.2). The bedrock, azure, openrouter, vercel
// and openai-compat wrappers override it with their own Name through
// WithName in their New. Anvil never calls WithName
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
		// and fantasy copies the fields through (anthropic languageModel
		// Generate and Stream). Bedrock uses the Anthropic language model.
	case openai.Name, azure.Name:
		// fantasy subtracts cached tokens from the prompt count for chat
		// completions (openai DefaultUsageFunc and DefaultStreamUsageFunc)
		// and the Responses API (openai responsesUsage). Azure uses the
		// same language models.
	case openrouter.Name:
		// openrouter languageModelUsage and languageModelStreamUsage
		// subtract cached tokens too.
	case vercel.Name:
		// vercel languageModelUsage and languageModelStreamUsage subtract
		// cached tokens too.
	case google.Name:
		// InputTokens is PromptTokenCount, which includes cached content
		// (google languageModel.mapUsage).
		//
		// Streaming sums CacheReadTokens across usage chunks while keeping
		// the first chunk's InputTokens (google languageModel.Stream and
		// streamObjectWithJSONMode), so CacheRead can exceed the real count
		// when Gemini repeats usage on several chunks. Cached tokens
		// cannot exceed the prompt, so CacheRead is capped at it. raw
		// keeps the reported values.
		tokens.CacheRead = min(tokens.CacheRead, reported.InputTokens)
		tokens.Input -= tokens.CacheRead
	case openaicompat.Name:
		// openai-compat uses fantasy's default OpenAI usage functions
		// (openai DefaultUsageFunc and DefaultStreamUsageFunc), which only
		// read prompt_tokens_details.cached_tokens. DeepSeek reports hits
		// in prompt_cache_hit_tokens instead, which those functions leave
		// in ExtraFields. Its prompt_tokens still includes the hits.
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
// the metadata by openai.Name even for openai-compat (openai
// DefaultStreamUsageFunc); the openai-compat key is checked as a fallback.
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

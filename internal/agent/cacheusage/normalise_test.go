package cacheusage

import (
	"encoding/json"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/stretchr/testify/require"
)

func openaiMeta(extra map[string]json.RawMessage) fantasy.ProviderMetadata {
	return fantasy.ProviderMetadata{
		openai.Name: &openai.ProviderMetadata{ExtraFields: extra},
	}
}

func TestNormalise(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		usage    fantasy.Usage
		meta     fantasy.ProviderMetadata
		want     Tokens
	}{
		{
			name:     "anthropic copies through",
			provider: "anthropic",
			usage:    fantasy.Usage{InputTokens: 10, CacheReadTokens: 900, CacheCreationTokens: 50, OutputTokens: 20, ReasoningTokens: 5},
			want:     Tokens{Input: 10, CacheRead: 900, CacheWrite: 50, Output: 20, Reasoning: 5},
		},
		{
			name:     "bedrock copies through",
			provider: "bedrock",
			usage:    fantasy.Usage{InputTokens: 10, CacheReadTokens: 90, CacheCreationTokens: 5},
			want:     Tokens{Input: 10, CacheRead: 90, CacheWrite: 5},
		},
		{
			name:     "openai copies through",
			provider: "openai",
			usage:    fantasy.Usage{InputTokens: 100, CacheReadTokens: 400, OutputTokens: 7},
			want:     Tokens{Input: 100, CacheRead: 400, Output: 7},
		},
		{
			name:     "azure copies through",
			provider: "azure",
			usage:    fantasy.Usage{InputTokens: 100, CacheReadTokens: 400},
			want:     Tokens{Input: 100, CacheRead: 400},
		},
		{
			name:     "openrouter copies through",
			provider: "openrouter",
			usage:    fantasy.Usage{InputTokens: 100, CacheReadTokens: 400},
			want:     Tokens{Input: 100, CacheRead: 400},
		},
		{
			name:     "vercel copies through",
			provider: "vercel",
			usage:    fantasy.Usage{InputTokens: 100, CacheReadTokens: 400},
			want:     Tokens{Input: 100, CacheRead: 400},
		},
		{
			name:     "google subtracts cache reads",
			provider: "google",
			usage:    fantasy.Usage{InputTokens: 1000, CacheReadTokens: 800, OutputTokens: 3},
			want:     Tokens{Input: 200, CacheRead: 800, Output: 3},
		},
		{
			name:     "google clamps at zero",
			provider: "google",
			usage:    fantasy.Usage{InputTokens: 100, CacheReadTokens: 300},
			want:     Tokens{Input: 0, CacheRead: 300},
		},
		{
			name:     "deepseek extra present",
			provider: "openai-compat",
			usage:    fantasy.Usage{InputTokens: 1000, OutputTokens: 4},
			meta:     openaiMeta(map[string]json.RawMessage{"prompt_cache_hit_tokens": json.RawMessage(`768`)}),
			want:     Tokens{Input: 232, CacheRead: 768, Output: 4},
		},
		{
			name:     "deepseek extra under openai-compat key",
			provider: "openai-compat",
			usage:    fantasy.Usage{InputTokens: 1000},
			meta: fantasy.ProviderMetadata{
				openaicompat.Name: &openai.ProviderMetadata{ExtraFields: map[string]json.RawMessage{"prompt_cache_hit_tokens": json.RawMessage(`10`)}},
			},
			want: Tokens{Input: 990, CacheRead: 10},
		},
		{
			name:     "deepseek extra absent",
			provider: "openai-compat",
			usage:    fantasy.Usage{InputTokens: 1000},
			meta:     openaiMeta(map[string]json.RawMessage{"prompt_cache_miss_tokens": json.RawMessage(`1000`)}),
			want:     Tokens{Input: 1000},
		},
		{
			name:     "deepseek no metadata",
			provider: "openai-compat",
			usage:    fantasy.Usage{InputTokens: 1000},
			want:     Tokens{Input: 1000},
		},
		{
			name:     "deepseek extra non-numeric",
			provider: "openai-compat",
			usage:    fantasy.Usage{InputTokens: 1000},
			meta:     openaiMeta(map[string]json.RawMessage{"prompt_cache_hit_tokens": json.RawMessage(`"768"`)}),
			want:     Tokens{Input: 1000},
		},
		{
			name:     "deepseek extra fractional",
			provider: "openai-compat",
			usage:    fantasy.Usage{InputTokens: 1000},
			meta:     openaiMeta(map[string]json.RawMessage{"prompt_cache_hit_tokens": json.RawMessage(`1.5`)}),
			want:     Tokens{Input: 1000},
		},
		{
			name:     "deepseek extra larger than input clamps",
			provider: "openai-compat",
			usage:    fantasy.Usage{InputTokens: 100},
			meta:     openaiMeta(map[string]json.RawMessage{"prompt_cache_hit_tokens": json.RawMessage(`500`)}),
			want:     Tokens{Input: 0, CacheRead: 500},
		},
		{
			name:     "openai-compat cached tokens already mapped wins",
			provider: "openai-compat",
			usage:    fantasy.Usage{InputTokens: 232, CacheReadTokens: 768},
			meta:     openaiMeta(map[string]json.RawMessage{"prompt_cache_hit_tokens": json.RawMessage(`768`)}),
			want:     Tokens{Input: 232, CacheRead: 768},
		},
		{
			name:     "deepseek extra ignored for other providers",
			provider: "openai",
			usage:    fantasy.Usage{InputTokens: 1000},
			meta:     openaiMeta(map[string]json.RawMessage{"prompt_cache_hit_tokens": json.RawMessage(`768`)}),
			want:     Tokens{Input: 1000},
		},
		{
			name:     "unknown provider copies through",
			provider: "kronk",
			usage:    fantasy.Usage{InputTokens: 100, CacheReadTokens: 40, CacheCreationTokens: 2},
			want:     Tokens{Input: 100, CacheRead: 40, CacheWrite: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _ := Normalise(tt.provider, tt.usage, tt.meta)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormaliseRawRoundTrips(t *testing.T) {
	t.Parallel()

	reported := fantasy.Usage{
		InputTokens:         1000,
		OutputTokens:        20,
		TotalTokens:         1020,
		ReasoningTokens:     3,
		CacheCreationTokens: 7,
		CacheReadTokens:     0,
	}
	extra := map[string]json.RawMessage{
		"prompt_cache_hit_tokens":  json.RawMessage(`768`),
		"prompt_cache_miss_tokens": json.RawMessage(`232`),
	}

	tokens, raw := Normalise("openai-compat", reported, openaiMeta(extra))
	require.Equal(t, int64(768), tokens.CacheRead)

	var decoded struct {
		Usage fantasy.Usage              `json:"usage"`
		Extra map[string]json.RawMessage `json:"extra"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	require.Equal(t, reported, decoded.Usage)
	require.Equal(t, extra, decoded.Extra)
}

func TestNormaliseRawNullExtra(t *testing.T) {
	t.Parallel()

	reported := fantasy.Usage{InputTokens: 5, CacheReadTokens: 6}
	_, raw := Normalise("anthropic", reported, nil)

	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	require.JSONEq(t, "null", string(decoded["extra"]))

	var usage fantasy.Usage
	require.NoError(t, json.Unmarshal(decoded["usage"], &usage))
	require.Equal(t, reported, usage)
}

func TestNormaliseRawInvalidExtra(t *testing.T) {
	t.Parallel()

	reported := fantasy.Usage{InputTokens: 5}
	_, raw := Normalise("openai", reported, openaiMeta(map[string]json.RawMessage{"bad": json.RawMessage(`{`)}))

	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	require.JSONEq(t, "null", string(decoded["extra"]))
	var usage fantasy.Usage
	require.NoError(t, json.Unmarshal(decoded["usage"], &usage))
	require.Equal(t, reported, usage)
}

func TestExtraFields(t *testing.T) {
	t.Parallel()

	extra := map[string]json.RawMessage{"k": json.RawMessage(`1`)}
	tests := []struct {
		name string
		meta fantasy.ProviderMetadata
		want map[string]json.RawMessage
	}{
		{name: "openai", meta: openaiMeta(extra), want: extra},
		{name: "openai-compat", meta: fantasy.ProviderMetadata{openaicompat.Name: &openai.ProviderMetadata{ExtraFields: extra}}, want: extra},
		{name: "nil metadata pointer", meta: fantasy.ProviderMetadata{openai.Name: (*openai.ProviderMetadata)(nil)}},
		{
			name: "empty openai falls back to openai-compat",
			meta: fantasy.ProviderMetadata{
				openai.Name:       &openai.ProviderMetadata{ExtraFields: map[string]json.RawMessage{}},
				openaicompat.Name: &openai.ProviderMetadata{ExtraFields: extra},
			},
			want: extra,
		},
		{name: "other metadata type", meta: fantasy.ProviderMetadata{openai.Name: &openai.ResponsesReasoningMetadata{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, extraFields(tt.meta))
		})
	}
}

func TestDeepSeekCacheHits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw    string
		want   int64
		wantOK bool
	}{
		{raw: `1`, want: 1, wantOK: true},
		{raw: `1e18`, want: 1e18, wantOK: true},
		{raw: `0`},
		{raw: `-1`},
		{raw: `1.5`},
		{raw: `9223372036854775807`},
		{raw: `"5"`},
	}
	for _, tt := range tests {
		got, ok := deepSeekCacheHits(map[string]json.RawMessage{deepSeekCacheHitField: json.RawMessage(tt.raw)})
		require.Equal(t, tt.wantOK, ok, tt.raw)
		require.Equal(t, tt.want, got, tt.raw)
	}
	_, ok := deepSeekCacheHits(nil)
	require.False(t, ok)
}

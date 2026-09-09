package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/discover"
	"github.com/qjebbs/go-jsons"
)

// reasoningRequest is what a provider's options need to know to switch the
// model's reasoning on or off.
type reasoningRequest struct {
	model      Model
	providerID string
	// effort is the effective reasoning effort, empty when the model can't
	// reason or has no reasoning levels.
	effort string
}

// wantsEffort reports whether the request has an effort to send under key
// and the user's options don't already set it.
func (r reasoningRequest) wantsEffort(opts map[string]any, key string) bool {
	_, has := opts[key]
	return !has && r.effort != ""
}

// thinking reports whether the user asked for thinking or picked an
// effort.
func (r reasoningRequest) thinking() bool {
	return r.model.ModelCfg.Think || r.effort != ""
}

// providerOptionsBuilder adds the reasoning settings to the merged user
// options and parses them into the provider's options type. The result is
// stored under name.
type providerOptionsBuilder struct {
	name  string
	build func(opts map[string]any, r reasoningRequest) (fantasy.ProviderOptionsData, error)
}

var providerOptionsBuilders = map[catwalk.Type]providerOptionsBuilder{
	openai.Name:       {openai.Name, openAIOptions},
	azure.Name:        {openai.Name, openAIOptions},
	anthropic.Name:    {anthropic.Name, anthropicOptions},
	bedrock.Name:      {anthropic.Name, anthropicOptions},
	openrouter.Name:   {openrouter.Name, openRouterOptions},
	vercel.Name:       {vercel.Name, vercelOptions},
	google.Name:       {google.Name, googleOptions},
	openaicompat.Name: {openaicompat.Name, openAICompatOptions},
}

// knownCustomProviderOptions serves the known custom providers (litellm,
// llamacpp, lmstudio, ollama, omlx), which are openai-compat under the
// hood.
var knownCustomProviderOptions = providerOptionsBuilder{
	name:  openaicompat.Name,
	build: knownCustomOptions,
}

func knownCustomOptions(opts map[string]any, r reasoningRequest) (fantasy.ProviderOptionsData, error) {
	if r.wantsEffort(opts, "reasoning_effort") {
		opts["reasoning_effort"] = r.effort
	}

	// Set "top_k" under "extra_body", as it is not part of the OpenAI
	// protocol and will be explicitly omitted by Fantasy downstream.
	topK := cmp.Or(r.model.ModelCfg.TopK, r.model.CatwalkCfg.Options.TopK)
	if topK != nil {
		extraBody, hasExtraBody := opts["extra_body"].(map[string]any)
		if !hasExtraBody {
			extraBody = make(map[string]any)
			opts["extra_body"] = extraBody
		}
		if _, hasTopK := extraBody["top_k"]; !hasTopK {
			extraBody["top_k"] = *topK
		}
	}

	parsed, err := openaicompat.ParseOptions(opts)
	if err == nil || topK == nil {
		return parsedOptions(parsed, err)
	}
	slog.Warn(
		"Failed to parse provider_options, falling back to top_k only",
		"provider", r.providerID,
		"error", err,
	)
	fallback, fallbackErr := openaicompat.ParseOptions(map[string]any{
		"extra_body": map[string]any{"top_k": *topK},
	})
	if fallbackErr != nil {
		slog.Warn(
			"Failed to parse fallback provider options, this should never happen",
			"provider", r.providerID,
			"error", fallbackErr,
		)
	}
	return parsedOptions(fallback, fallbackErr)
}

// callTopK returns topK for use on fantasy.Call.TopK, suppressing it for
// known custom providers: getProviderOptions already carries top_k for
// them via extra_body, and passing it here too makes Fantasy emit a
// spurious "top_k unsupported" warning for every turn.
func callTopK(providerCfg config.ProviderConfig, topK *int64) *int64 {
	if discover.IsKnownCustomProvider(string(providerCfg.Type)) {
		return nil
	}
	return topK
}

func getProviderOptions(model Model, providerCfg config.ProviderConfig) fantasy.ProviderOptions {
	options := fantasy.ProviderOptions{}

	// Later layers override earlier ones: catwalk defaults, then the
	// provider config, then the model config.
	layers := []map[string]any{
		model.CatwalkCfg.Options.ProviderOptions,
		providerCfg.ProviderOptions,
		model.ModelCfg.ProviderOptions,
	}
	readers := make([]io.Reader, 0, len(layers))
	for _, layer := range layers {
		data := []byte("{}")
		if encoded, err := json.Marshal(layer); layer != nil && err == nil {
			data = encoded
		}
		readers = append(readers, bytes.NewReader(data))
	}
	got, err := jsons.Merge(readers)
	if err != nil {
		slog.Error("Could not merge call config", "err", err)
		return options
	}
	mergedOptions := make(map[string]any)
	if err := json.Unmarshal([]byte(got), &mergedOptions); err != nil {
		slog.Error("Could not create config for call", "err", err)
		return options
	}

	builder, ok := providerOptionsBuilders[providerCfg.Type]
	if !ok {
		if !discover.IsKnownCustomProvider(string(providerCfg.Type)) {
			return options
		}
		builder = knownCustomProviderOptions
	}
	parsed, err := builder.build(mergedOptions, reasoningRequest{
		model:      model,
		providerID: providerCfg.ID,
		effort:     effectiveReasoningEffort(model),
	})
	if err == nil {
		options[builder.name] = parsed
	}
	return options
}

// parsedOptions adapts a provider's ParseOptions result to the common
// options type.
func parsedOptions[T fantasy.ProviderOptionsData](opts T, err error) (fantasy.ProviderOptionsData, error) {
	return opts, err
}

// thinkingType is the {"type": "enabled"|"disabled"} toggle several
// providers take in place of an effort.
func thinkingType(on bool) map[string]any {
	if on {
		return map[string]any{"type": "enabled"}
	}
	return map[string]any{"type": "disabled"}
}

func openAIOptions(opts map[string]any, r reasoningRequest) (fantasy.ProviderOptionsData, error) {
	if r.wantsEffort(opts, "reasoning_effort") {
		opts["reasoning_effort"] = r.effort
	}
	id := r.model.CatwalkCfg.ID
	if !openai.IsResponsesModel(id) {
		return parsedOptions(openai.ParseOptions(opts))
	}
	if openai.IsResponsesReasoningModel(id) {
		opts["reasoning_summary"] = "auto"
		opts["include"] = []openai.IncludeType{openai.IncludeReasoningEncryptedContent}
	}
	return parsedOptions(openai.ParseResponsesOptions(opts))
}

func anthropicOptions(opts map[string]any, r reasoningRequest) (fantasy.ProviderOptionsData, error) {
	reasoning, ok := anthropicReasoning[r.providerID]
	if !ok {
		reasoning = standardAnthropicReasoning
	}
	reasoning(opts, r)
	return parsedOptions(anthropic.ParseOptions(opts))
}

// anthropicReasoning holds how each Anthropic-compatible provider that
// doesn't take Anthropic's own "effort" and "thinking" fields is told to
// reason. Providers not listed use standardAnthropicReasoning.
var anthropicReasoning = map[string]func(opts map[string]any, r reasoningRequest){
	string(catwalk.InferenceProviderAlibabaSingapore): alibabaAnthropicReasoning,
	string(catwalk.InferenceProviderAlibabaUS):        alibabaAnthropicReasoning,
}

func standardAnthropicReasoning(opts map[string]any, r reasoningRequest) {
	_, hasThink := opts["thinking"]
	if r.wantsEffort(opts, "effort") {
		opts["effort"] = r.effort
	} else if !hasThink && r.model.ModelCfg.Think {
		opts["thinking"] = map[string]any{"budget_tokens": 2000}
	}
}

func alibabaAnthropicReasoning(opts map[string]any, r reasoningRequest) {
	_, hasThink := opts["thinking"]
	extraBody := make(map[string]any)
	if r.wantsEffort(opts, "effort") {
		extraBody["reasoning_effort"] = r.effort
	} else if !hasThink && r.model.CatwalkCfg.CanReason {
		extraBody["thinking"] = thinkingType(r.model.ModelCfg.Think)
	}
	opts["extra_body"] = extraBody
}

func openRouterOptions(opts map[string]any, r reasoningRequest) (fantasy.ProviderOptionsData, error) {
	if r.wantsEffort(opts, "reasoning") {
		opts["reasoning"] = map[string]any{"enabled": true, "effort": r.effort}
	}
	return parsedOptions(openrouter.ParseOptions(opts))
}

func vercelOptions(opts map[string]any, r reasoningRequest) (fantasy.ProviderOptionsData, error) {
	if r.wantsEffort(opts, "reasoning") {
		opts["reasoning"] = map[string]any{"enabled": true, "effort": r.effort}
	}
	return parsedOptions(vercel.ParseOptions(opts))
}

func googleOptions(opts map[string]any, r reasoningRequest) (fantasy.ProviderOptionsData, error) {
	if _, has := opts["thinking_config"]; !has {
		if strings.HasPrefix(r.model.CatwalkCfg.ID, "gemini-2") {
			opts["thinking_config"] = map[string]any{"thinking_budget": 2000, "include_thoughts": true}
		} else {
			opts["thinking_config"] = map[string]any{"thinking_level": r.effort, "include_thoughts": true}
		}
	}
	return parsedOptions(google.ParseOptions(opts))
}

func openAICompatOptions(opts map[string]any, r reasoningRequest) (fantasy.ProviderOptionsData, error) {
	extraBody := make(map[string]any)
	reasoning, ok := openAICompatReasoning[r.providerID]
	if !ok {
		reasoning = standardReasoningEffort
	}
	reasoning(opts, extraBody, r)
	opts["extra_body"] = extraBody
	return parsedOptions(openaicompat.ParseOptions(opts))
}

// openAICompatReasoning holds how each openai-compat provider is told to
// reason. "reasoning_effort" is a standard OpenAI field, but "thinking" is
// not, so each provider that takes it sets it its own way. Providers not
// listed use standardReasoningEffort.
// TODO: Abstract this in Fantasy somehow?
// TODO: Allow custom providers to specify how to set this?
var openAICompatReasoning = map[string]func(opts, extraBody map[string]any, r reasoningRequest){
	string(catwalk.InferenceProviderIoNet): func(opts, extraBody map[string]any, r reasoningRequest) {
		if r.wantsEffort(opts, "reasoning_effort") {
			extraBody["reasoning"] = map[string]string{"effort": r.effort}
		} else if r.model.CatwalkCfg.CanReason {
			if r.model.ModelCfg.Think {
				extraBody["reasoning"] = map[string]string{"effort": "medium"}
			} else {
				extraBody["reasoning"] = map[string]string{"effort": "none"}
			}
		}
	},
	string(catwalk.InferenceProviderOpenCodeGo):  openCodeReasoning,
	string(catwalk.InferenceProviderOpenCodeZen): openCodeReasoning,
	string(catwalk.InferenceProviderZAI):         zaiReasoning,
	string(catwalk.InferenceProviderDeepSeek):    zaiReasoning,
	string(catwalk.InferenceProviderFireworks): func(opts, extraBody map[string]any, r reasoningRequest) {
		standardReasoningEffort(opts, extraBody, r)
		// NOTE: Fireworks break if we set both `reasoning_effort` and `thinking`.
		if r.model.ModelCfg.ReasoningEffort == "" {
			extraBody["thinking"] = thinkingType(r.model.ModelCfg.Think)
		}
	},
	string(catwalk.InferenceProviderBaseten): func(opts, extraBody map[string]any, r reasoningRequest) {
		standardReasoningEffort(opts, extraBody, r)
		extraBody["chat_template_args"] = map[string]any{
			"enable_thinking": r.model.ModelCfg.Think || r.effort != "" && r.effort != "none",
		}
	},
	string(catwalk.InferenceProviderAlibabaSingapore): alibabaReasoning,
	string(catwalk.InferenceProviderAlibabaUS):        alibabaReasoning,
}

func standardReasoningEffort(opts, _ map[string]any, r reasoningRequest) {
	if r.wantsEffort(opts, "reasoning_effort") {
		opts["reasoning_effort"] = r.effort
	}
}

// openCodeReasoning serves OpenCode Go and Zen. MiniMax models there use
// the "thinking" parameter instead of "reasoning_effort", and need
// "reasoning_split" so thinking content is returned in the
// "reasoning_content" field instead of inline in "content". Other models
// use the standard field.
func openCodeReasoning(opts, extraBody map[string]any, r reasoningRequest) {
	if !strings.HasPrefix(strings.ToLower(r.model.CatwalkCfg.ID), "minimax") {
		standardReasoningEffort(opts, extraBody, r)
		return
	}
	if r.model.CatwalkCfg.CanReason && r.thinking() {
		extraBody["thinking"] = map[string]any{"type": "adaptive"}
		extraBody["reasoning_split"] = true
	} else {
		extraBody["thinking"] = thinkingType(false)
	}
}

func zaiReasoning(opts, extraBody map[string]any, r reasoningRequest) {
	standardReasoningEffort(opts, extraBody, r)
	extraBody["thinking"] = thinkingType(r.thinking())
}

func alibabaReasoning(opts, extraBody map[string]any, r reasoningRequest) {
	standardReasoningEffort(opts, extraBody, r)
	if r.model.CatwalkCfg.CanReason {
		extraBody["enable_thinking"] = r.thinking()
	}
}

// effectiveReasoningEffort returns the reasoning effort to apply for
// provider calls. It prefers the user-selected effort when valid,
// otherwise the model default when valid, and finally falls back to the
// first configured reasoning level.
func effectiveReasoningEffort(model Model) string {
	if !model.CatwalkCfg.CanReason {
		return ""
	}

	if effort := model.ModelCfg.ReasoningEffort; effort != "" && slices.Contains(model.CatwalkCfg.ReasoningLevels, effort) {
		return effort
	}
	if effort := model.CatwalkCfg.DefaultReasoningEffort; effort != "" && slices.Contains(model.CatwalkCfg.ReasoningLevels, effort) {
		return effort
	}
	if len(model.CatwalkCfg.ReasoningLevels) > 0 {
		return model.CatwalkCfg.ReasoningLevels[0]
	}
	return ""
}

func mergeCallOptions(model Model, cfg config.ProviderConfig) (fantasy.ProviderOptions, *float64, *float64, *int64, *float64, *float64) {
	modelOptions := getProviderOptions(model, cfg)
	temp := cmp.Or(model.ModelCfg.Temperature, model.CatwalkCfg.Options.Temperature)
	topP := cmp.Or(model.ModelCfg.TopP, model.CatwalkCfg.Options.TopP)
	topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
	freqPenalty := cmp.Or(model.ModelCfg.FrequencyPenalty, model.CatwalkCfg.Options.FrequencyPenalty)
	presPenalty := cmp.Or(model.ModelCfg.PresencePenalty, model.CatwalkCfg.Options.PresencePenalty)
	return modelOptions, temp, topP, topK, freqPenalty, presPenalty
}

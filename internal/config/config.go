package config

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	anthropicoauth "github.com/Broderick-Westrope/anvil/internal/oauth/anthropic"
	"github.com/Broderick-Westrope/anvil/internal/oauth/copilot"
)

const (
	appName                 = "anvil"
	defaultProjectDirectory = ".anvil"
	defaultInitializeAs     = "AGENTS.md"
)

var defaultContextPaths = []string{
	".github/copilot-instructions.md",
	".cursorrules",
	".cursor/rules/",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"GEMINI.md",
	"gemini.md",
	"anvil.md",
	"anvil.local.md",
	"Anvil.md",
	"Anvil.local.md",
	"ANVIL.md",
	"ANVIL.local.md",
	"AGENTS.md",
	"agents.md",
	"Agents.md",
}

type SelectedModelType string

// String returns the string representation of the [SelectedModelType].
func (s SelectedModelType) String() string {
	return string(s)
}

const (
	SelectedModelTypeLarge SelectedModelType = "large"
	SelectedModelTypeSmall SelectedModelType = "small"
)

const (
	AgentOrchestrator string = "orchestrator"
)

type SelectedModel struct {
	// The model id as used by the provider API.
	// Required.
	Model string `json:"model" jsonschema:"required,description=The model ID as used by the provider API,example=gpt-4o"`
	// The model provider, same as the key/id used in the providers config.
	// Required.
	Provider string `json:"provider" jsonschema:"required,description=The model provider ID that matches a key in the providers config,example=openai"`

	// Variant is an optional model variant passthrough (e.g. thinking budget).
	Variant string `json:"variant,omitempty" jsonschema:"description=Optional model variant passthrough (e.g. thinking budget)"`

	// Only used by models that use the openai provider and need this set.
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"description=Reasoning effort level for OpenAI models that support it,enum=low,enum=medium,enum=high"`

	// Used by anthropic models that can reason to indicate if the model should think.
	Think bool `json:"think,omitempty" jsonschema:"description=Enable thinking mode for Anthropic models that support reasoning"`

	// Overrides the default model configuration.
	MaxTokens        int64    `json:"max_tokens,omitempty" jsonschema:"description=Maximum number of tokens for model responses,maximum=200000,example=4096"`
	Temperature      *float64 `json:"temperature,omitempty" jsonschema:"description=Sampling temperature,minimum=0,maximum=1,example=0.7"`
	TopP             *float64 `json:"top_p,omitempty" jsonschema:"description=Top-p (nucleus) sampling parameter,minimum=0,maximum=1,example=0.9"`
	TopK             *int64   `json:"top_k,omitempty" jsonschema:"description=Top-k sampling parameter"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty" jsonschema:"description=Frequency penalty to reduce repetition"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty" jsonschema:"description=Presence penalty to increase topic diversity"`

	// Override provider specific options.
	ProviderOptions map[string]any `json:"provider_options,omitempty" jsonschema:"description=Additional provider-specific options for the model"`
}

type ProviderConfig struct {
	// The provider's id.
	ID string `json:"id,omitempty" jsonschema:"description=Unique identifier for the provider,example=openai"`
	// The provider's name, used for display purposes.
	Name string `json:"name,omitempty" jsonschema:"description=Human-readable name for the provider,example=OpenAI"`
	// The provider's API endpoint.
	BaseURL string `json:"base_url,omitempty" jsonschema:"description=Base URL for the provider's API,format=uri,example=https://api.openai.com/v1"`
	// The provider type, e.g. "openai", "anthropic", etc. if empty it defaults to openai.
	Type catwalk.Type `json:"type,omitempty" jsonschema:"description=Provider type that determines the API format,enum=openai,enum=openai-compat,enum=anthropic,enum=gemini,enum=azure,enum=vertexai,default=openai"`
	// Authentication mode: "oauth", "api-key", or "" (auto-detect,
	// default). When empty, OAuth is preferred over API key for
	// providers that support it (e.g. Anthropic).
	AuthMode AuthMode `json:"auth_mode,omitempty" jsonschema:"description=Authentication mode: oauth or api-key. When empty the provider auto-detects (preferring OAuth),enum=oauth,enum=api-key"`
	// The provider's API key.
	APIKey string `json:"api_key,omitempty" jsonschema:"description=API key for authentication with the provider,example=$OPENAI_API_KEY"`
	// The original API key template before resolution (for re-resolution on auth errors).
	APIKeyTemplate string `json:"-"`
	// OAuthToken for providers that use OAuth2 authentication.
	OAuthToken *oauth.Token `json:"oauth,omitempty" jsonschema:"description=OAuth2 token for authentication with the provider"`
	// Marks the provider as disabled.
	Disable bool `json:"disable,omitempty" jsonschema:"description=Whether this provider is disabled,default=false"`

	// Custom system prompt prefix.
	SystemPromptPrefix string `json:"system_prompt_prefix,omitempty" jsonschema:"description=Custom prefix to add to system prompts for this provider"`

	// Extra headers to send with each request to the provider. Values
	// run through shell expansion at config-load time, so $VAR and
	// $(cmd) work the same way they do in MCP headers. A header whose
	// value resolves to the empty string (unset bare $VAR under
	// lenient nounset, $(echo), or literal "") is omitted from the
	// outgoing request rather than sent as "Header:". See PLAN.md
	// Phase 2 design decision #18.
	ExtraHeaders map[string]string `json:"extra_headers,omitempty" jsonschema:"description=Additional HTTP headers to send with requests"`
	// ExtraBody is merged verbatim into OpenAI-compatible request
	// bodies. String values are NOT shell-expanded: this is a plain
	// JSON passthrough so that arbitrary provider-extension fields
	// (numbers, nested objects, booleans) round-trip without a
	// recursive walker guessing at intent. If you need an env-var-
	// driven value at request time, put it in extra_headers, or in
	// the provider's top-level api_key / base_url, all of which do
	// expand. See PLAN.md Phase 2 design decision #16.
	ExtraBody map[string]any `json:"extra_body,omitempty" jsonschema:"description=Additional fields to include in request bodies\\, only works with openai-compatible providers"`

	ProviderOptions map[string]any `json:"provider_options,omitempty" jsonschema:"description=Additional provider-specific options for this provider"`

	// Used to pass extra parameters to the provider.
	ExtraParams map[string]string `json:"-"`

	// Skip cost accumulation for this provider when using subscription or flat rate billing.
	FlatRate bool `json:"flat_rate,omitempty" jsonschema:"description=Flat-rate mode for this provider"`

	// The provider models
	Models []catwalk.Model `json:"models,omitempty" jsonschema:"description=List of models available from this provider"`
}

// ToProvider converts the [ProviderConfig] to a [catwalk.Provider].
func (c *ProviderConfig) ToProvider() catwalk.Provider {
	// Convert config provider to provider.Provider format
	provider := catwalk.Provider{
		Name:   c.Name,
		ID:     catwalk.InferenceProvider(c.ID),
		Models: make([]catwalk.Model, len(c.Models)),
	}

	// Convert models
	for i, model := range c.Models {
		provider.Models[i] = catwalk.Model{
			ID:                     model.ID,
			Name:                   model.Name,
			CostPer1MIn:            model.CostPer1MIn,
			CostPer1MOut:           model.CostPer1MOut,
			CostPer1MInCached:      model.CostPer1MInCached,
			CostPer1MOutCached:     model.CostPer1MOutCached,
			ContextWindow:          model.ContextWindow,
			DefaultMaxTokens:       model.DefaultMaxTokens,
			CanReason:              model.CanReason,
			ReasoningLevels:        model.ReasoningLevels,
			DefaultReasoningEffort: model.DefaultReasoningEffort,
			SupportsImages:         model.SupportsImages,
		}
	}

	return provider
}

func (c *ProviderConfig) SetupGitHubCopilot() {
	maps.Copy(c.ExtraHeaders, copilot.Headers())
}

// SetupAnthropic configures the Bearer API key and OAuth headers for an
// Anthropic provider authenticated via OAuth. No-op when there is no
// OAuth token.
func (c *ProviderConfig) SetupAnthropic() {
	if c.OAuthToken == nil {
		return
	}
	c.APIKey = "Bearer " + c.OAuthToken.AccessToken
	if c.ExtraHeaders == nil {
		c.ExtraHeaders = make(map[string]string)
	}
	maps.Copy(c.ExtraHeaders, anthropicoauth.Headers())
}

// AuthMode controls how a provider authenticates requests.
type AuthMode string

const (
	// AuthModeAuto auto-detects, preferring OAuth when available.
	AuthModeAuto AuthMode = ""
	// AuthModeOAuth forces OAuth authentication.
	AuthModeOAuth AuthMode = "oauth"
	// AuthModeAPIKey forces API key authentication.
	AuthModeAPIKey AuthMode = "api-key"
)

type MCPType string

const (
	MCPStdio MCPType = "stdio"
	MCPSSE   MCPType = "sse"
	MCPHttp  MCPType = "http"
)

// MCPAuthType identifies the authentication method for an MCP server.
type MCPAuthType string

const (
	// MCPAuthNone means no authentication (the default).
	MCPAuthNone MCPAuthType = ""
	// MCPAuthOAuth enables OAuth 2.0 authentication for HTTP/SSE servers.
	MCPAuthOAuth MCPAuthType = "oauth"
)

type MCPConfig struct {
	Command       string            `json:"command,omitempty" jsonschema:"description=Command to execute for stdio MCP servers,example=npx"`
	Env           map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set for the MCP server"`
	Args          []string          `json:"args,omitempty" jsonschema:"description=Arguments to pass to the MCP server command"`
	Type          MCPType           `json:"type" jsonschema:"required,description=Type of MCP connection,enum=stdio,enum=sse,enum=http,default=stdio"`
	URL           string            `json:"url,omitempty" jsonschema:"description=URL for HTTP or SSE MCP servers,format=uri,example=http://localhost:3000/mcp"`
	Disabled      bool              `json:"disabled,omitempty" jsonschema:"description=Whether this MCP server is disabled,default=false"`
	DisabledTools []string          `json:"disabled_tools,omitempty" jsonschema:"description=List of tools from this MCP server to disable,example=get-library-doc"`
	EnabledTools  []string          `json:"enabled_tools,omitempty" jsonschema:"description=Allow list of tools from this MCP server,example=get-library-doc"`
	Timeout       int               `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for MCP server connections,default=15,example=30,example=60,example=120"`

	// Headers are HTTP headers for HTTP/SSE MCP servers. Values run
	// through shell expansion at MCP startup, so $VAR and $(cmd)
	// work. A header whose value resolves to the empty string (unset
	// bare $VAR under lenient nounset, $(echo), or literal "") is
	// omitted from the outgoing request rather than sent as
	// "Header:". See PLAN.md Phase 2 design decision #18.
	Headers map[string]string `json:"headers,omitempty" jsonschema:"description=HTTP headers for HTTP/SSE MCP servers"`

	// Auth selects the authentication method. Only "oauth" is
	// supported and only for HTTP/SSE servers.
	Auth         MCPAuthType `json:"auth,omitempty" jsonschema:"description=Authentication method for HTTP/SSE MCP servers,enum=oauth"`
	ClientID     string      `json:"clientId,omitempty" jsonschema:"description=OAuth client ID for pre-registered clients"`
	ClientSecret string      `json:"clientSecret,omitempty" jsonschema:"description=OAuth client secret (supports shell expansion via $VAR or $(cmd))"`
	Scopes       []string    `json:"scopes,omitempty" jsonschema:"description=OAuth scopes to request during authorization"`
	RedirectURI  string      `json:"redirectUri,omitempty" jsonschema:"description=Fixed OAuth redirect URI for pre-registered clients (e.g. http://localhost:3118/callback)"`

	// LazyDescription makes this MCP server's tools lazy-loaded. The
	// server still connects eagerly at startup, but its tools and
	// instructions are excluded from the LLM context until explicitly
	// enabled by the agent or human. The value is surfaced to the LLM
	// so it can decide when to enable the server.
	LazyDescription string `json:"lazy_description,omitempty" jsonschema:"description=Short description shown to the LLM; when set the server's tools are hidden until explicitly enabled"`
}

// IsLazy reports whether the MCP server's tools are lazy-loaded.
func (m MCPConfig) IsLazy() bool {
	return m.LazyDescription != ""
}

type LSPConfig struct {
	Disabled    bool              `json:"disabled,omitempty" jsonschema:"description=Whether this LSP server is disabled,default=false"`
	Command     string            `json:"command,omitempty" jsonschema:"description=Command to execute for the LSP server,example=gopls"`
	Args        []string          `json:"args,omitempty" jsonschema:"description=Arguments to pass to the LSP server command"`
	Env         map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set to the LSP server command"`
	FileTypes   []string          `json:"filetypes,omitempty" jsonschema:"description=File types this LSP server handles,example=go,example=mod,example=rs,example=c,example=js,example=ts"`
	RootMarkers []string          `json:"root_markers,omitempty" jsonschema:"description=Files or directories that indicate the project root,example=go.mod,example=package.json,example=Cargo.toml"`
	InitOptions map[string]any    `json:"init_options,omitempty" jsonschema:"description=Initialization options passed to the LSP server during initialize request"`
	Options     map[string]any    `json:"options,omitempty" jsonschema:"description=LSP server-specific settings passed during initialization"`
	Timeout     int               `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for LSP server initialization,default=30,example=60,example=120"`
}

type TUIOptions struct {
	CompactMode bool   `json:"compact_mode,omitempty" jsonschema:"description=Enable compact mode for the TUI interface,default=false"`
	DiffMode    string `json:"diff_mode,omitempty" jsonschema:"description=Diff mode for the TUI interface,enum=unified,enum=split"`
	// Here we can add themes later or any TUI related options
	//

	Completions Completions `json:"completions,omitzero" jsonschema:"description=Completions UI options"`
	Transparent *bool       `json:"transparent,omitempty" jsonschema:"description=Enable transparent background for the TUI interface,default=false"`
}

// Completions defines options for the completions UI.
type Completions struct {
	MaxDepth *int `json:"max_depth,omitempty" jsonschema:"description=Maximum depth for the ls tool,default=0,example=10"`
	MaxItems *int `json:"max_items,omitempty" jsonschema:"description=Maximum number of items to return for the ls tool,default=1000,example=100"`
}

func (c Completions) Limits() (depth, items int) {
	return ptrValOr(c.MaxDepth, 0), ptrValOr(c.MaxItems, 0)
}

type Permissions struct {
	AllowedTools []string `json:"allowed_tools,omitempty" jsonschema:"description=List of tools that don't require permission prompts,example=bash,example=view"`
}

type Options struct {
	ContextPaths         []string    `json:"context_paths,omitempty" jsonschema:"description=Paths to files containing context information for the AI,example=.cursorrules,example=ANVIL.md"`
	SkillsPaths          []string    `json:"skills_paths,omitempty" jsonschema:"description=Paths to directories containing Agent Skills (folders with SKILL.md files),example=~/.config/anvil/skills,example=./skills"`
	TUI                  *TUIOptions `json:"tui,omitempty" jsonschema:"description=Terminal user interface options"`
	Debug                bool        `json:"debug,omitempty" jsonschema:"description=Enable debug logging,default=false"`
	DebugLSP             bool        `json:"debug_lsp,omitempty" jsonschema:"description=Enable debug logging for LSP servers,default=false"`
	DisableAutoSummarize bool        `json:"disable_auto_summarize,omitempty" jsonschema:"description=Disable automatic conversation summarization,default=false"`
	// ProjectDirectory is where Anvil keeps per-project state such as
	// logs, workspace config, and .gitignore. Relative paths are
	// resolved against the working directory; absolute paths are used
	// verbatim. After defaulting the stored value is always absolute.
	ProjectDirectory          string   `json:"project_directory,omitempty" jsonschema:"description=Directory for per-project state (logs\\, workspace config\\, .gitignore). Relative paths are resolved against the working directory; absolute paths are used as-is.,default=.anvil,example=.anvil"`
	DeprecatedDataDirectory   string   `json:"data_directory,omitempty" jsonschema:"-"`
	DisabledTools             []string `json:"disabled_tools,omitempty" jsonschema:"description=List of built-in tools to disable and hide from the agent,example=bash,example=sourcegraph"`
	DisableProviderAutoUpdate bool     `json:"disable_provider_auto_update,omitempty" jsonschema:"description=Disable providers auto-update,default=false"`
	DisableDefaultProviders   bool     `json:"disable_default_providers,omitempty" jsonschema:"description=Ignore all default/embedded providers. When enabled\\, providers must be fully specified in the config file with base_url\\, models\\, and api_key - no merging with defaults occurs,default=false"`
	InitializeAs              string   `json:"initialize_as,omitempty" jsonschema:"description=Name of the context file to create/update during project initialization,default=AGENTS.md,example=AGENTS.md,example=ANVIL.md,example=CLAUDE.md,example=docs/LLMs.md"`
	AutoLSP                   *bool    `json:"auto_lsp,omitempty" jsonschema:"description=Automatically setup LSPs based on root markers,default=true"`
	Progress                  *bool    `json:"progress,omitempty" jsonschema:"description=Show indeterminate progress updates during long operations,default=true"`
	DisableNotifications      bool     `json:"disable_notifications,omitempty" jsonschema:"description=Disable desktop notifications,default=false"`
	DisabledSkills            []string `json:"disabled_skills,omitempty" jsonschema:"description=List of skill names to disable and hide from the agent,example=anvil-config"`
	ExpandedTools             []string `json:"expanded_tools,omitempty" jsonschema:"description=Glob patterns for tools that should render expanded instead of compact,example=bash,example=mcp_*"`
}

type MCPs map[string]MCPConfig

type MCP struct {
	Name string    `json:"name"`
	MCP  MCPConfig `json:"mcp"`
}

func (m MCPs) Sorted() []MCP {
	sorted := make([]MCP, 0, len(m))
	for k, v := range m {
		sorted = append(sorted, MCP{
			Name: k,
			MCP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b MCP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

type LSPs map[string]LSPConfig

type LSP struct {
	Name string    `json:"name"`
	LSP  LSPConfig `json:"lsp"`
}

func (l LSPs) Sorted() []LSP {
	sorted := make([]LSP, 0, len(l))
	for k, v := range l {
		sorted = append(sorted, LSP{
			Name: k,
			LSP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b LSP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

// ResolvedEnv returns m.Env with every value expanded through the
// given resolver. The returned slice is of the form "KEY=value" sorted
// by key so callers get deterministic output; the receiver's Env map is
// not mutated. On the first resolution failure it returns nil and an
// error that identifies the offending key; the inner resolver error is
// already sanitized by ResolveValue and is wrapped with %w so
// errors.Is/As continues to work. Callers are expected to surface it
// (for MCP, via StateError on the status card) rather than silently
// spawn the server with an empty credential.
//
// The resolver choice matters: in server mode pass the shell resolver
// so $VAR / $(cmd) expand; in client mode pass IdentityResolver so the
// template is forwarded verbatim and expansion happens on the server.
func (m MCPConfig) ResolvedEnv(r VariableResolver) ([]string, error) {
	return resolveEnvs(m.Env, r)
}

// ResolvedArgs returns m.Args with every element expanded through the
// given resolver. A fresh slice is allocated; m.Args is never mutated.
// On the first resolution failure it returns nil and an error
// identifying the offending positional index; the inner resolver error
// is already sanitized by ResolveValue and is wrapped with %w so
// errors.Is/As continues to work.
//
// See ResolvedEnv for guidance on picking a resolver.
func (m MCPConfig) ResolvedArgs(r VariableResolver) ([]string, error) {
	if len(m.Args) == 0 {
		return nil, nil
	}
	out := make([]string, len(m.Args))
	for i, a := range m.Args {
		v, err := r.ResolveValue(a)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// ResolvedURL returns m.URL expanded through the given resolver. The
// receiver is not mutated. Errors from the resolver are already
// sanitized by ResolveValue and are wrapped with %w for errors.Is/As.
//
// URLs run through the same shell-expansion pipeline as the other
// fields, so a literal '$' (e.g. OData query strings containing
// $filter/$select) must be escaped as '\$' or '${DOLLAR:-$}' to avoid
// being interpreted as a variable reference. Same constraint already
// applies to command, args, env, and headers.
//
// See ResolvedEnv for guidance on picking a resolver.
func (m MCPConfig) ResolvedURL(r VariableResolver) (string, error) {
	if m.URL == "" {
		return "", nil
	}
	v, err := r.ResolveValue(m.URL)
	if err != nil {
		return "", fmt.Errorf("url: %w", err)
	}
	return v, nil
}

// ResolvedClientSecret returns m.ClientSecret expanded through the
// given resolver. An empty ClientSecret short-circuits without
// calling the resolver. See ResolvedURL for the expansion contract.
func (m MCPConfig) ResolvedClientSecret(r VariableResolver) (string, error) {
	if m.ClientSecret == "" {
		return "", nil
	}
	v, err := r.ResolveValue(m.ClientSecret)
	if err != nil {
		return "", fmt.Errorf("clientSecret: %w", err)
	}
	return v, nil
}

// ResolvedHeaders returns m.Headers with every value expanded through
// the given resolver. A fresh map is allocated; m.Headers is never
// mutated. On the first resolution failure it returns nil and an error
// identifying the offending header name; the inner resolver error is
// already sanitized by ResolveValue and is wrapped with %w so
// errors.Is/As continues to work.
//
// A header whose value resolves to the empty string (unset bare $VAR
// under lenient nounset, $(echo), or literal "") is omitted from the
// returned map — sending "X-Auth:" with an empty value is rejected by
// some providers and the user's intent in "optional, env-gated
// header" is clearly "absent when the var isn't set." See PLAN.md
// Phase 2 design decision #18.
//
// See ResolvedEnv for guidance on picking a resolver.
func (m MCPConfig) ResolvedHeaders(r VariableResolver) (map[string]string, error) {
	if len(m.Headers) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(m.Headers))
	// Sort keys so failures are reported deterministically when more
	// than one header would fail.
	keys := make([]string, 0, len(m.Headers))
	for k := range m.Headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v, err := r.ResolveValue(m.Headers[k])
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", k, err)
		}
		if v == "" {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// ResolvedArgs returns l.Args with every element expanded through the
// given resolver. A fresh slice is allocated; l.Args is never mutated.
// On the first resolution failure it returns nil and an error
// identifying the offending positional index; the inner resolver error
// is already sanitized by ResolveValue and is wrapped with %w so
// errors.Is/As continues to work.
//
// Empty resolved values are kept (a deliberate "empty positional arg"
// like --flag "" is sometimes valid), matching MCPConfig.ResolvedArgs;
// see PLAN.md Phase 2 design decision #18.
//
// The resolver choice matters: in server mode pass the shell resolver
// so $VAR / $(cmd) expand; in client mode pass IdentityResolver so the
// template is forwarded verbatim. See PLAN.md Phase 2 design decision
// #13.
func (l LSPConfig) ResolvedArgs(r VariableResolver) ([]string, error) {
	if len(l.Args) == 0 {
		return nil, nil
	}
	out := make([]string, len(l.Args))
	for i, a := range l.Args {
		v, err := r.ResolveValue(a)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// ResolvedEnv returns l.Env with every value expanded through the
// given resolver. A fresh map is allocated; l.Env is never mutated.
// On the first resolution failure it returns nil and an error that
// identifies the offending key; the inner resolver error is already
// sanitized by ResolveValue and is wrapped with %w so errors.Is/As
// continues to work.
//
// Empty resolved values are kept ("FOO=" is a legitimate request;
// opt out via ${VAR:+...}), matching MCPConfig.ResolvedEnv; see
// PLAN.md Phase 2 design decision #18.
//
// Shape note: this returns map[string]string rather than the []string
// shape MCPConfig.ResolvedEnv uses because the consumer
// (powernap.ClientConfig.Environment in internal/lsp/client.go) takes
// a map directly — returning a []string here would only force a
// round-trip back to a map at the call site. See PLAN.md Phase 2
// design decision #13.
//
// See ResolvedArgs for guidance on picking a resolver.
func (l LSPConfig) ResolvedEnv(r VariableResolver) (map[string]string, error) {
	if len(l.Env) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(l.Env))
	// Sort keys so failures are reported deterministically when more
	// than one value would fail.
	keys := make([]string, 0, len(l.Env))
	for k := range l.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v, err := r.ResolveValue(l.Env[k])
		if err != nil {
			return nil, fmt.Errorf("env %q: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

// Agent defines the configuration for a named agent in the multi-agent
// system. Fields left at their zero value fall back to global defaults.
type Agent struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`

	// Model is the full provider/model string (e.g. "anthropic/claude-opus-4-6").
	// An empty string falls back to the global large model.
	Model string `json:"model,omitempty"`

	// Variant is the model variant (e.g. thinking budget passthrough).
	Variant string `json:"variant,omitempty"`

	// AllowedTools is the list of tools available to the agent.
	// nil means all tools are available; [] means no tools.
	AllowedTools []string `json:"tools,omitempty"`

	// AllowedSkills is the list of skill names available to the agent.
	// nil means all skills are available; [] means no skills.
	AllowedSkills []string `json:"skills,omitempty"`

	// AllowedMCP is the map of MCP server names to allowed tool names.
	// An empty map means no MCPs; a nil map means all MCPs are available.
	// Each value slice lists the allowed tools from that MCP server;
	// a nil value means all tools from that server are available.
	// This field is populated by UnmarshalJSON which accepts either
	// []string or map[string][]string for the "mcps" JSON key.
	AllowedMCP map[string][]string `json:"-"`

	// AppendPrompt is injected verbatim at the end of the agent's system
	// prompt.
	AppendPrompt string `json:"append_prompt,omitempty"`
}

// agentJSON is an alias used inside UnmarshalJSON to prevent recursion.
type agentJSON Agent

// UnmarshalJSON implements custom JSON unmarshalling for Agent. It handles
// the "mcps" field which may be either a []string (each name mapped to a nil
// tool list) or a map[string][]string.
func (a *Agent) UnmarshalJSON(data []byte) error {
	aux := struct {
		*agentJSON
		MCPs json.RawMessage `json:"mcps,omitempty"`
	}{
		agentJSON: (*agentJSON)(a),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.MCPs) == 0 {
		return nil
	}
	// Try []string first.
	var names []string
	if err := json.Unmarshal(aux.MCPs, &names); err == nil {
		if err := ValidateFilterList(names); err != nil {
			return fmt.Errorf("mcps: %w", err)
		}
		a.AllowedMCP = make(map[string][]string, len(names))
		for _, name := range names {
			a.AllowedMCP[name] = nil
		}
		return nil
	}
	// Fall back to map[string][]string.
	var mcpMap map[string][]string
	if err := json.Unmarshal(aux.MCPs, &mcpMap); err != nil {
		return fmt.Errorf("mcps field must be []string or map[string][]string: %w", err)
	}
	a.AllowedMCP = mcpMap
	return nil
}

type Tools struct {
	Ls   ToolLs   `json:"ls,omitzero"`
	Grep ToolGrep `json:"grep,omitzero"`
	Glob ToolGlob `json:"glob,omitzero"`
}

type ToolLs struct {
	MaxDepth *int `json:"max_depth,omitempty" jsonschema:"description=Maximum depth for the ls tool,default=0,example=10"`
	MaxItems *int `json:"max_items,omitempty" jsonschema:"description=Maximum number of items to return for the ls tool,default=1000,example=100"`
}

// Limits returns the user-defined max-depth and max-items, or their defaults.
func (t ToolLs) Limits() (depth, items int) {
	return ptrValOr(t.MaxDepth, 0), ptrValOr(t.MaxItems, 0)
}

type ToolGrep struct {
	Timeout *time.Duration `json:"timeout,omitempty" jsonschema:"description=Timeout for the grep tool call,default=5s,example=10s"`
}

// GetTimeout returns the user-defined timeout or the default.
func (t ToolGrep) GetTimeout() time.Duration {
	return ptrValOr(t.Timeout, 5*time.Second)
}

type ToolGlob struct {
	Timeout *time.Duration `json:"timeout,omitempty" jsonschema:"description=Timeout for the glob tool call,default=30s,example=10s"`
}

// GetTimeout returns the user-defined timeout or the default.
func (t ToolGlob) GetTimeout() time.Duration {
	return ptrValOr(t.Timeout, 30*time.Second)
}

// PluginConfig defines an external plugin directory that provides skills,
// commands, and/or agent definitions.
type PluginConfig struct {
	// Path is the filesystem path to the plugin directory. Supports ~ and
	// environment variable expansion.
	Path string `json:"path" jsonschema:"description=Path to the plugin directory"`
}

// HookConfig defines a user-configured shell command that fires on a hook
// event (e.g. PreToolUse). This is a pure-data struct: matcher compilation
// is owned by hooks.Runner so a JSON round-trip, merge, or reload can't
// silently drop compiled state.
type HookConfig struct {
	// Friendly display name shown in the TUI. Falls back to Command when empty.
	Name string `json:"name,omitempty" jsonschema:"description=Friendly display name shown in the TUI for this hook"`
	// Regex pattern tested against the tool name. Empty means match all.
	Matcher string `json:"matcher,omitempty" jsonschema:"description=Regex pattern tested against the tool name. Empty means match all tools."`
	// Shell command to execute.
	Command string `json:"command" jsonschema:"required,description=Shell command to execute when the hook fires"`
	// Timeout in seconds. Default 30.
	Timeout int `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for the hook command,default=30"`
}

// DisplayName returns the hook name for display purposes. It returns Name
// when set, otherwise falls back to Command.
func (h *HookConfig) DisplayName() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Command
}

// TimeoutDuration returns the hook timeout as a time.Duration, defaulting
// to 30s.
func (h *HookConfig) TimeoutDuration() time.Duration {
	if h.Timeout <= 0 {
		return 30 * time.Second
	}
	return time.Duration(h.Timeout) * time.Second
}

// Config holds the configuration for anvil.
type Config struct {
	Schema string `json:"$schema,omitempty"`

	// We currently only support large/small as values here.
	Models map[SelectedModelType]SelectedModel `json:"models,omitempty" jsonschema:"description=Model configurations for different model types,example={\"large\":{\"model\":\"gpt-4o\",\"provider\":\"openai\"}}"`

	// Recently used models stored in the data directory config.
	RecentModels map[SelectedModelType][]SelectedModel `json:"recent_models,omitempty" jsonschema:"-"`

	// The providers that are configured
	Providers *csync.Map[string, ProviderConfig] `json:"providers,omitempty" jsonschema:"description=AI provider configurations"`

	MCP MCPs `json:"mcp,omitempty" jsonschema:"description=Model Context Protocol server configurations"`

	LSP LSPs `json:"lsp,omitempty" jsonschema:"description=Language Server Protocol configurations"`

	Options *Options `json:"options,omitempty" jsonschema:"description=General application options"`

	Permissions *Permissions `json:"permissions,omitempty" jsonschema:"description=Permission settings for tool usage"`

	Tools Tools `json:"tools,omitzero" jsonschema:"description=Tool configurations"`

	Hooks map[string][]HookConfig `json:"hooks,omitempty" jsonschema:"description=User-defined shell commands that fire on hook events (e.g. PreToolUse)"`

	// Agents is the map of named agent configurations. After SetupAgents,
	// only the orchestrator is present. The full roster is populated by
	// SetupAgentsWithDefaults when the coordinator loads .md files.
	Agents map[string]Agent `json:"agents,omitempty"`

	// userAgentOverrides stores the raw user-provided agent overrides from
	// anvil.json before SetupAgents overwrites c.Agents with defaults. This
	// allows SetupAgentsWithDefaults (called later by the coordinator) to
	// re-apply user overrides on top of .md-derived defaults.
	userAgentOverrides map[string]Agent

	// agentsInitialized is set true by SetupAgents on its first call to
	// guard against SetupAgentsWithDefaults running before it.
	agentsInitialized bool

	// DisabledAgents lists agent names to remove from the routing table and
	// orchestrator prompt at startup.
	DisabledAgents []string `json:"disabled_agents,omitempty"`

	// Plugins is a list of external plugin directories to load skills,
	// commands, and agents from.
	Plugins []PluginConfig `json:"plugins,omitempty" jsonschema:"description=External plugin directories"`
}

// cloneForWrite returns a copy of c that the store's typed field mutators
// may modify without racing readers of the currently published Config.
//
// Reads of a published Config take no lock beyond the pointer load, so a
// mutator must never write through the live pointer. Instead it clones,
// mutates the clone, and atomically swaps it in. The clone gives fresh
// copies of every field a typed mutator touches in place — Models,
// RecentModels, MCP, and Options (with its nested TUI pointer). Providers
// is a *csync.Map (internally synchronized) and is shared by reference;
// the remaining fields are immutable after load from the mutators'
// standpoint and are likewise shared.
func (c *Config) cloneForWrite() *Config {
	nc := *c
	nc.Models = maps.Clone(c.Models)
	nc.RecentModels = maps.Clone(c.RecentModels)
	nc.MCP = maps.Clone(c.MCP)
	if c.Options != nil {
		opts := *c.Options
		if c.Options.TUI != nil {
			tui := *c.Options.TUI
			opts.TUI = &tui
		}
		nc.Options = &opts
	}
	return &nc
}

// ensureTUI returns c.Options.TUI, allocating Options and TUI as needed so
// callers can assign TUI fields without nil checks.
func (c *Config) ensureTUI() *TUIOptions {
	if c.Options == nil {
		c.Options = &Options{}
	}
	if c.Options.TUI == nil {
		c.Options.TUI = &TUIOptions{}
	}
	return c.Options.TUI
}

func (c *Config) EnabledProviders() []ProviderConfig {
	var enabled []ProviderConfig
	for p := range c.Providers.Seq() {
		if !p.Disable {
			enabled = append(enabled, p)
		}
	}
	return enabled
}

// IsConfigured  return true if at least one provider is configured
func (c *Config) IsConfigured() bool {
	return len(c.EnabledProviders()) > 0
}

func (c *Config) GetModel(provider, model string) *catwalk.Model {
	if providerConfig, ok := c.Providers.Get(provider); ok {
		for _, m := range providerConfig.Models {
			if m.ID == model {
				return &m
			}
		}
	}
	return nil
}

func (c *Config) GetProviderForModel(modelType SelectedModelType) *ProviderConfig {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	if providerConfig, ok := c.Providers.Get(model.Provider); ok {
		return &providerConfig
	}
	return nil
}

func (c *Config) GetModelByType(modelType SelectedModelType) *catwalk.Model {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) LargeModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeLarge]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) SmallModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeSmall]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

const maxRecentModelsPerType = 5

func allToolNames() []string {
	return []string{
		"task",
		"bash",
		"anvil_info",
		"anvil_logs",
		"job_output",
		"job_kill",
		"download",
		"edit",
		"multiedit",
		"lsp_diagnostics",
		"lsp_references",
		"lsp_restart",
		"lsp_symbols",
		"lsp_definition",
		"lsp_call_hierarchy",
		"lsp_rename",
		"lsp_replace_symbol",
		"fetch",
		"agentic_fetch",
		"glob",
		"grep",
		"ls",
		"sourcegraph",
		"todos",
		"view",
		"write",
		"list_mcp_resources",
		"read_mcp_resource",
	}
}

// readOnlyTools returns the base read-only tool names for agents that
// should only be able to inspect the codebase without modifying it.
func readOnlyTools() []string {
	return []string{
		"glob",
		"grep",
		"ls",
		"view",
		"lsp_diagnostics",
		"lsp_references",
		"lsp_definition",
		"lsp_symbols",
		"lsp_call_hierarchy",
		"sourcegraph",
	}
}

// setupDefaultAgents initialises Config.Agents with only the orchestrator
// default. The orchestrator has no .md file, so its config is always hardcoded.
// Other agent defaults are sourced from .md files at coordinator init via
// SetupAgentsWithDefaults.
func (c *Config) setupDefaultAgents() {
	c.Agents = map[string]Agent{
		AgentOrchestrator: {
			ID:            AgentOrchestrator,
			Name:          "Orchestrator",
			AllowedTools:  nil, // Nil = all tools unrestricted.
			AllowedSkills: nil, // Nil = all skills unrestricted.
			AllowedMCP:    nil, // Nil = all MCPs unrestricted.
		},
	}
}

// SetupAgents sets up the orchestrator agent default and stores the raw user
// overrides from anvil.json for later use by SetupAgentsWithDefaults. Non-
// orchestrator agents are loaded from .md files by the coordinator. On the
// first call, the current c.Agents value is snapshotted as user overrides;
// subsequent calls (e.g. config reload) reuse the original snapshot.
func (c *Config) SetupAgents() {
	// Only snapshot user overrides on the first call. Subsequent calls (e.g.
	// config reload) reuse the original snapshot to avoid capturing processed
	// defaults from a prior SetupAgentsWithDefaults call.
	if !c.agentsInitialized {
		c.userAgentOverrides = c.Agents
	}

	// Start with only the orchestrator. Non-orchestrator agent defaults are
	// sourced from .md files at coordinator init via SetupAgentsWithDefaults.
	c.setupDefaultAgents()

	c.applyAgentOverrides(c.userAgentOverrides)

	c.agentsInitialized = true
}

// SetupAgentsWithDefaults returns a new agent map built from .md-derived
// defaults, plus the orchestrator default, plus any user overrides from
// anvil.json. mdDefaults contains agents parsed from .md frontmatter (keyed
// by agent name). The orchestrator is always added from internal defaults
// since it has no .md file. This must be called after SetupAgents (which
// stores the raw user overrides). The coordinator calls this after parsing
// .md files. The receiver is not mutated.
func (c *Config) SetupAgentsWithDefaults(mdDefaults map[string]Agent) map[string]Agent {
	// Guard: SetupAgents must have been called first to store user overrides.
	if !c.agentsInitialized {
		slog.Warn("SetupAgentsWithDefaults called before SetupAgents; user overrides may be lost")
	}

	// Start with only the orchestrator default on a fresh map.
	agents := map[string]Agent{
		AgentOrchestrator: {
			ID:            AgentOrchestrator,
			Name:          "Orchestrator",
			AllowedTools:  nil, // Nil = all tools unrestricted.
			AllowedSkills: nil, // Nil = all skills unrestricted.
			AllowedMCP:    nil, // Nil = all MCPs unrestricted.
		},
	}

	// Merge in .md-derived defaults. The orchestrator is already present so
	// mdDefaults entries with the orchestrator key are silently ignored.
	for name, agent := range mdDefaults {
		if _, exists := agents[name]; !exists {
			agents[name] = agent
		}
	}

	// Apply the raw user overrides stored by SetupAgents, not c.Agents which
	// now contains the full hardcoded roster.
	agents = applyOverrides(agents, c.userAgentOverrides, c.DisabledAgents)

	return agents
}

// applyAgentOverrides overlays non-zero fields from userAgents onto the
// current c.Agents map and removes any disabled agents. It is used by
// SetupAgents.
func (c *Config) applyAgentOverrides(userAgents map[string]Agent) {
	c.Agents = applyOverrides(c.Agents, userAgents, c.DisabledAgents)
}

// applyOverrides overlays non-zero fields from userAgents onto agents and
// removes disabled agents. It returns the modified map.
func applyOverrides(agents map[string]Agent, userAgents map[string]Agent, disabledAgents []string) map[string]Agent {
	if userAgents != nil {
		for name, userAgent := range userAgents {
			def, ok := agents[name]
			if !ok {
				// User defined an agent not in the defaults; add it as-is.
				// Ensure ID and Name are set from the map key if not provided.
				if userAgent.ID == "" {
					userAgent.ID = name
				}
				if userAgent.Name == "" {
					userAgent.Name = name
				}
				agents[name] = userAgent
				continue
			}
			// Overlay non-zero fields from the user config onto the default.
			if userAgent.Model != "" {
				def.Model = userAgent.Model
			}
			if userAgent.Variant != "" {
				def.Variant = userAgent.Variant
			}
			if userAgent.AllowedTools != nil {
				def.AllowedTools = userAgent.AllowedTools
			}
			if userAgent.AllowedSkills != nil {
				def.AllowedSkills = userAgent.AllowedSkills
			}
			if userAgent.AllowedMCP != nil {
				def.AllowedMCP = userAgent.AllowedMCP
			}
			if userAgent.AppendPrompt != "" {
				def.AppendPrompt = userAgent.AppendPrompt
			}
			if userAgent.Disabled {
				def.Disabled = true
			}
			agents[name] = def
		}
	}

	// Remove any agents whose names appear in disabledAgents and agents
	// whose Disabled field is true.
	for _, name := range disabledAgents {
		delete(agents, name)
	}
	for name, agent := range agents {
		if agent.Disabled {
			delete(agents, name)
		}
	}

	return agents
}

// ResolveAgentModel resolves the SelectedModel for the given agent. If
// agent.Model is empty the global large model from cfg is returned. Otherwise
// agent.Model is parsed as "provider/model" and resolved against the
// configured providers. If agent.Variant is set it is included in the
// returned SelectedModel.
func ResolveAgentModel(agent Agent, cfg *Config) (SelectedModel, error) {
	if agent.Model == "" {
		m, ok := cfg.Models[SelectedModelTypeLarge]
		if !ok {
			return SelectedModel{}, fmt.Errorf("agent %q: no large model configured", agent.ID)
		}
		if agent.Variant != "" {
			m.Variant = agent.Variant
		}
		return m, nil
	}

	// Parse "provider/model" format; split on the first slash only.
	slash := strings.IndexByte(agent.Model, '/')
	if slash < 0 {
		return SelectedModel{}, fmt.Errorf(
			"agent %q: model %q must be in provider/model format",
			agent.ID, agent.Model,
		)
	}
	providerID := agent.Model[:slash]
	modelID := agent.Model[slash+1:]

	providerCfg, ok := cfg.Providers.Get(providerID)
	if !ok {
		return SelectedModel{}, fmt.Errorf(
			"agent %q: provider %q not found",
			agent.ID, providerID,
		)
	}

	var found *catwalk.Model
	for i := range providerCfg.Models {
		if providerCfg.Models[i].ID == modelID {
			m := providerCfg.Models[i]
			found = &m
			break
		}
	}
	if found == nil {
		return SelectedModel{}, fmt.Errorf(
			"agent %q: model %q not found in provider %q",
			agent.ID, modelID, providerID,
		)
	}

	result := SelectedModel{
		Provider:  providerID,
		Model:     modelID,
		MaxTokens: found.DefaultMaxTokens,
	}
	if found.DefaultReasoningEffort != "" {
		result.ReasoningEffort = found.DefaultReasoningEffort
	}
	if agent.Variant != "" {
		result.Variant = agent.Variant
	}
	return result, nil
}

func (c *ProviderConfig) TestConnection(resolver VariableResolver) error {
	var (
		providerID = catwalk.InferenceProvider(c.ID)
		testURL    = ""
		headers    = make(map[string]string)
		apiKey, _  = resolver.ResolveValue(c.APIKey)
	)

	switch providerID {
	case catwalk.InferenceProviderMiniMax, catwalk.InferenceProviderMiniMaxChina:
		// NOTE: MiniMax has no good endpoint we can use to validate the API key.
		return nil
	}

	switch c.Type {
	case catwalk.TypeOpenAI, catwalk.TypeOpenAICompat, catwalk.TypeOpenRouter:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://api.openai.com/v1")

		switch providerID {
		case catwalk.InferenceProviderOpenRouter:
			testURL = baseURL + "/credits"
		case catwalk.InferenceProviderOpenCodeGo:
			testURL = strings.Replace(baseURL, "/go", "", 1) + "/models"
		default:
			testURL = baseURL + "/models"
		}

		headers["Authorization"] = "Bearer " + apiKey
	case catwalk.TypeAnthropic:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://api.anthropic.com/v1")

		switch providerID {
		case catwalk.InferenceKimiCoding:
			testURL = baseURL + "/v1/models"
		default:
			testURL = baseURL + "/models"
		}

		// OAuth tokens are presented as Bearer credentials; plain API
		// keys use the x-api-key header expected by the Anthropic REST
		// API.
		if strings.HasPrefix(apiKey, "Bearer ") {
			headers["Authorization"] = apiKey
		} else {
			headers["x-api-key"] = apiKey
		}
		headers["anthropic-version"] = "2023-06-01"
	case catwalk.TypeGoogle:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://generativelanguage.googleapis.com")
		testURL = baseURL + "/v1beta/models?key=" + url.QueryEscape(apiKey)
	case catwalk.TypeBedrock:
		// NOTE: Bedrock has a `/foundation-models` endpoint that we could in
		// theory use, but apparently the authorization is region-specific,
		// so it's not so trivial.
		if strings.HasPrefix(apiKey, "ABSK") { // Bedrock API keys
			return nil
		}
		return errors.New("not a valid bedrock api key")
	case catwalk.TypeVercel:
		// NOTE: Vercel does not validate API keys on the `/models` endpoint.
		if strings.HasPrefix(apiKey, "vck_") { // Vercel API keys
			return nil
		}
		return errors.New("not a valid vercel api key")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	defer resp.Body.Close()

	switch providerID {
	case catwalk.InferenceProviderZAI:
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("failed to connect to provider %s: %s", c.ID, resp.Status)
		}
	default:
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to connect to provider %s: %s", c.ID, resp.Status)
		}
	}
	return nil
}

// resolveEnvs expands every value in envs through the given resolver
// and returns a fresh "KEY=value" slice sorted by key. The input map is
// not mutated. On the first resolution failure it returns nil and an
// error identifying the offending variable; the inner resolver error is
// already sanitized by ResolveValue and is wrapped with %w.
func resolveEnvs(envs map[string]string, r VariableResolver) ([]string, error) {
	if len(envs) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	res := make([]string, 0, len(envs))
	for _, k := range keys {
		v, err := r.ResolveValue(envs[k])
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		res = append(res, fmt.Sprintf("%s=%s", k, v))
	}
	return res, nil
}

func ptrValOr[T any](t *T, el T) T {
	if t == nil {
		return el
	}
	return *t
}

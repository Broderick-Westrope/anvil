package agent

import (
	"cmp"
	"database/sql"
	"os"
	"strconv"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"github.com/Broderick-Westrope/anvil/internal/agent/cacheusage"
)

// Kinds of recorded model calls.
const (
	usageKindTurn    = "turn"
	usageKindSummary = "summary"
	usageKindTitle   = "title"
	usageKindSmall   = "small"
)

// Cache policies recorded per row.
const (
	cachePolicyAnthropicEphemeral = "anthropic_ephemeral"
	cachePolicyDisabled           = "disabled"
	cachePolicyAutomatic          = "automatic"
	cachePolicyNone               = "none"
)

// reviewerAgentName labels CompleteSmall rows; its only caller is the
// bouncer's small-model reviewer (internal/app/bouncer.go).
const reviewerAgentName = "reviewer"

// turnPrefix is the history fingerprint of a turn step: how many
// non-system messages it sent and their rolling hash.
type turnPrefix struct {
	count int
	hash  string
}

// stepCapture collects facts for one model request and turns them into a
// cacheusage.Row when the provider reports usage. It is owned by one
// goroutine, so it needs no lock.
type stepCapture struct {
	runID     string
	kind      string
	agent     string
	sessionID string
	parentID  string
	messageID string
	stepIndex int
	attempt   int
	model     Model
	started   time.Time
	retries   int
	fp        cacheusage.Fingerprint
	prefix    *bool // Nil when unknown.
	// messages are the messages sent, kept to estimate input tokens when
	// the provider reports no usage.
	messages []fantasy.Message
}

// newCapture starts a capture for one request. It fingerprints tools and
// messages, so call it only when a.usageRecorder is non-nil.
func (a *sessionAgent) newCapture(kind, runID, sessionID, parentID string, model Model, tools []fantasy.AgentTool, messages []fantasy.Message) *stepCapture {
	return &stepCapture{
		runID:     runID,
		kind:      kind,
		agent:     a.agentName,
		sessionID: sessionID,
		parentID:  parentID,
		model:     model,
		started:   time.Now(),
		fp:        cacheusage.Compute(tools, messages),
		messages:  messages,
	}
}

// comparePrefix sets c.prefix by checking c's history against prev.
func (c *stepCapture) comparePrefix(prev turnPrefix) {
	if match, ok := c.fp.PrefixMatches(prev.count, prev.hash); ok {
		c.prefix = &match
	}
}

// turnPrefix returns the history fingerprint later steps compare against.
func (c *stepCapture) turnPrefix() turnPrefix {
	return turnPrefix{count: c.fp.MessageCount, hash: c.fp.HistoryHash}
}

// captureCallbacks returns OnRetry and OnStreamFinish callbacks that count
// retries on, and record, whichever capture *capture points to when they
// fire. They are for calls whose callbacks all run on one goroutine.
// Recording never fails the stream.
func (a *sessionAgent) captureCallbacks(capture **stepCapture) (fantasy.OnRetryCallback, fantasy.OnStreamFinishFunc) {
	onRetry := func(*fantasy.ProviderError, time.Duration) {
		if *capture != nil {
			(*capture).retries++
		}
	}
	onStreamFinish := func(usage fantasy.Usage, reason fantasy.FinishReason, meta fantasy.ProviderMetadata) error {
		if *capture != nil {
			a.usageRecorder.Record(a.newRow(*capture, usage, reason, meta, time.Now()))
		}
		return nil
	}
	return onRetry, onStreamFinish
}

func (a *sessionAgent) newRow(c *stepCapture, usage fantasy.Usage, reason fantasy.FinishReason, meta fantasy.ProviderMetadata, finished time.Time) cacheusage.Row {
	var providerType, modelID string
	if c.model.Model != nil {
		providerType = c.model.Model.Provider()
		modelID = c.model.Model.Model()
	}
	tokens, raw := cacheusage.Normalise(providerType, usage, meta)

	var estimated int64
	if usageIsZero(usage) {
		estimated = 1
		tokens.Input = estimateMessageTokens(c.messages)
	}

	var prefix sql.NullInt64
	if c.prefix != nil {
		prefix.Valid = true
		if *c.prefix {
			prefix.Int64 = 1
		}
	}

	var flatRate int64
	if c.model.FlatRate {
		flatRate = 1
	}

	cw := c.model.CatwalkCfg
	return cacheusage.Row{
		SessionID:          c.sessionID,
		ParentSessionID:    c.parentID,
		WorkingDir:         a.workingDir,
		MessageID:          c.messageID,
		Agent:              c.agent,
		Kind:               c.kind,
		Depth:              int64(a.depth),
		RunID:              c.runID,
		StepIndex:          int64(c.stepIndex),
		Attempt:            int64(c.attempt),
		Provider:           cmp.Or(c.model.ModelCfg.Provider, providerType),
		ProviderType:       providerType,
		Model:              cmp.Or(c.model.ModelCfg.Model, modelID),
		RequestStartedAt:   c.started.UnixMilli(),
		ResponseFinishedAt: finished.UnixMilli(),
		RetryCount:         int64(c.retries),
		FinishReason:       string(reason),
		InputTokens:        tokens.Input,
		CacheReadTokens:    tokens.CacheRead,
		CacheWriteTokens:   tokens.CacheWrite,
		OutputTokens:       tokens.Output,
		ReasoningTokens:    tokens.Reasoning,
		Estimated:          estimated,
		RawUsage:           raw,
		PriceInput:         cw.CostPer1MIn,
		PriceOutput:        cw.CostPer1MOut,
		// catwalk's CostPer1MOutCached is the cache read price and
		// CostPer1MInCached the cache write price (see cacheusage.Row).
		PriceCacheRead:     cw.CostPer1MOutCached,
		PriceCacheWrite:    cw.CostPer1MInCached,
		FlatRate:           flatRate,
		CachePolicy:        cachePolicy(providerType),
		MessageCount:       int64(c.fp.MessageCount),
		SystemCount:        int64(c.fp.SystemCount),
		ToolCount:          int64(c.fp.ToolCount),
		ToolsHash:          c.fp.ToolsHash,
		SystemHash:         c.fp.SystemHash,
		HistoryHash:        c.fp.HistoryHash,
		HistoryPrefixMatch: prefix,
		FingerprintError:   c.fp.Err,
	}
}

// cachePolicy mirrors getCacheControlOptions: Anthropic-style providers get
// explicit ephemeral markers unless ANVIL_DISABLE_ANTHROPIC_CACHE is set,
// while OpenAI-style providers and Google cache automatically and ignore
// the markers and the override.
func cachePolicy(providerType string) string {
	switch providerType {
	case anthropic.Name, bedrock.Name, vercel.Name:
		if disabled, _ := strconv.ParseBool(os.Getenv("ANVIL_DISABLE_ANTHROPIC_CACHE")); disabled {
			return cachePolicyDisabled
		}
		return cachePolicyAnthropicEphemeral
	case openai.Name, azure.Name, openaicompat.Name, openrouter.Name, google.Name:
		return cachePolicyAutomatic
	default:
		return cachePolicyNone
	}
}

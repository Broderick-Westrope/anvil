package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/bouncer"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/systemone"
)

const (
	defaultBouncerTimeoutSeconds = 8
	maxIntentParentDepth         = 3
	intentBranchTail             = 50
	// bouncerWarmTimeout bounds the startup warm-up call. Serverless
	// deployments can take tens of seconds to cold start.
	bouncerWarmTimeout = 2 * time.Minute
)

// bouncerSetup is the result of building the bouncer from config.
type bouncerSetup struct {
	option  permission.Option
	bouncer *bouncer.Bouncer
	mode    permission.BouncerMode
}

// buildBouncerOption turns the trusted bouncer config into a permission
// option. It builds the option even when the mode is off so a runtime
// toggle can enable the bouncer later.
func buildBouncerOption(ta *config.TrustedBouncer, sessions session.Service, messages message.Service) (bouncerSetup, bool) {
	if ta == nil || ta.Config == nil {
		return bouncerSetup{}, false
	}
	cfg := ta.Config
	var missing []string
	if cfg.URL == "" {
		missing = append(missing, "url")
	}
	if cfg.Model == "" {
		missing = append(missing, "model")
	}
	if ta.APIKey == "" {
		missing = append(missing, "api key ($"+cmp.Or(cfg.APIKeyEnv, config.DefaultBouncerAPIKeyEnv)+")")
	}
	if len(missing) > 0 {
		slog.Warn("Bouncer is configured but unusable", "missing", missing)
		return bouncerSetup{}, false
	}

	sendUserMessages := cfg.SendUserMessages == nil || *cfg.SendUserMessages
	client := &systemone.Client{
		URL:        cfg.URL,
		APIKey:     ta.APIKey,
		AuthScheme: string(cmp.Or(cfg.AuthScheme, config.BouncerAuthAPIKey)),
		Model:      cfg.Model,
		HTTP:       &http.Client{},
		Backoff:    []time.Duration{250 * time.Millisecond},
	}
	a, err := bouncer.New(client, BouncerThresholds(cfg), sendUserMessages)
	if err != nil {
		slog.Warn("Bouncer thresholds are invalid", "error", err)
		return bouncerSetup{}, false
	}
	opts := permission.BouncerOptions{
		Bouncer:            a,
		Mode:               permission.BouncerMode(cmp.Or(cfg.Mode, config.BouncerOff)),
		Timeout:            time.Duration(cmp.Or(cfg.TimeoutSeconds, defaultBouncerTimeoutSeconds)) * time.Second,
		ExplicitAskToHuman: cfg.ExplicitAsk == config.BouncerExplicitAskHuman,
		// The permission service calls Warm only when switching the mode
		// from off to on, so the target mode is never off here.
		Warm: func(ctx context.Context) { warmBouncer(ctx, a, permission.BouncerEnforce) },
	}
	if sendUserMessages {
		opts.Intent = &intentSource{sessions: sessions, messages: messages}
	}
	return bouncerSetup{option: permission.WithBouncer(opts), bouncer: a, mode: opts.Mode}, true
}

// warmer is the part of the bouncer the startup warm-up needs.
type warmer interface {
	Warm(ctx context.Context) error
}

// warmBouncer primes a cold serverless deployment so the first real
// assessment isn't slowed by a cold start. It does nothing when the mode
// is off, because off promises no network calls. Failures only log: the
// bouncer still fails to the human on its own.
func warmBouncer(ctx context.Context, w warmer, mode permission.BouncerMode) {
	if w == nil || mode == permission.BouncerOff {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, bouncerWarmTimeout)
	defer cancel()
	start := time.Now()
	if err := w.Warm(ctx); err != nil {
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			slog.Warn("Bouncer warm-up failed", "error", err, "elapsed", time.Since(start).Round(time.Millisecond))
		}
		return
	}
	slog.Info("Bouncer warmed", "elapsed", time.Since(start).Round(time.Millisecond))
}

// ApplyConfig pushes the parts of a reloaded config that services copied
// at startup into those services: the permission rules and the bouncer's
// thresholds. The allow cache is reset only when the thresholds change,
// since a cached allow was decided under the old ones.
func (app *App) ApplyConfig(cur *config.Config, curB *config.TrustedBouncer) error {
	var rules []config.PermissionRule
	if cur != nil && cur.Permissions != nil {
		rules = cur.Permissions.Rules
	}
	app.Permissions.SetConfigRules(rules)

	if app.bouncer == nil || curB == nil || curB.Config == nil {
		return nil
	}
	th := BouncerThresholds(curB.Config)
	if reflect.DeepEqual(th, app.bouncer.Thresholds()) {
		return nil
	}
	if err := app.bouncer.SetThresholds(th); err != nil {
		return err
	}
	app.Permissions.ResetBouncerCache()
	return nil
}

// BouncerThresholds overlays the thresholds set in cfg on the bouncer's
// defaults.
func BouncerThresholds(cfg *config.Bouncer) bouncer.Thresholds {
	th := bouncer.DefaultThresholds()
	if cfg.EscalateAt != nil {
		for axis := range th.EscalateAt {
			th.EscalateAt[axis] = *cfg.EscalateAt
		}
	}
	maps.Copy(th.EscalateAt, cfg.EscalateAtAxes)
	if cfg.ConcernAt != nil {
		th.ConcernAt = *cfg.ConcernAt
	}
	if cfg.SeverityConcern != nil {
		th.SeverityConcern = *cfg.SeverityConcern
	}
	if cfg.DenyAt != nil {
		th.DenyAt = *cfg.DenyAt
	}
	if cfg.SeverityEscalate != nil {
		th.SeverityEscalate = *cfg.SeverityEscalate
	}
	if cfg.UserRequestedAt != nil {
		th.UserRequestedAt = *cfg.UserRequestedAt
	}
	return th
}

// intentSource reads recent user messages from the active branch of a
// session, resolving subagent sessions to the session that spawned them.
type intentSource struct {
	sessions session.Service
	messages message.Service
}

var _ permission.IntentSource = (*intentSource)(nil)

func (s *intentSource) RecentUserMessages(ctx context.Context, sessionID string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	sess, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	for range maxIntentParentDepth {
		if sess.ParentSessionID == "" {
			break
		}
		sess, err = s.sessions.Get(ctx, sess.ParentSessionID)
		if err != nil {
			return nil, fmt.Errorf("load parent session: %w", err)
		}
	}
	if sess.LeafMessageID == "" {
		return nil, nil
	}

	// GetBranchPathTail returns the branch oldest-first (root to leaf).
	path, err := s.messages.GetBranchPathTail(ctx, sess.LeafMessageID, intentBranchTail)
	if err != nil {
		return nil, fmt.Errorf("load branch: %w", err)
	}
	var texts []string
	for i := range path {
		if path[i].Role != message.User {
			continue
		}
		// Job event notices are written by Anvil and quote background job
		// output, so they say nothing about what the user asked for.
		if path[i].MessageType == message.MessageTypeJobEvent {
			continue
		}
		if text := path[i].Content().Text; text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) > n {
		texts = texts[len(texts)-n:]
	}
	return texts, nil
}

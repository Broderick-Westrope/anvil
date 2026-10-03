package app

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/assessor"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

const (
	defaultAssessorTimeoutSeconds = 8
	maxIntentParentDepth          = 3
	intentBranchTail              = 50
)

// buildAssessorOption turns the trusted assessor config into a permission
// option. It builds the option even when the mode is off so a runtime
// toggle can enable the assessor later.
func buildAssessorOption(ta *config.TrustedAssessor, sessions session.Service, messages message.Service) (permission.Option, bool) {
	if ta == nil || ta.Config == nil {
		return nil, false
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
		missing = append(missing, "api key ($"+cmp.Or(cfg.APIKeyEnv, config.DefaultAssessorAPIKeyEnv)+")")
	}
	if len(missing) > 0 {
		slog.Warn("Permission assessor is configured but unusable", "missing", missing)
		return nil, false
	}

	th := assessorThresholds(cfg)
	if err := th.Validate(); err != nil {
		slog.Warn("Permission assessor thresholds are invalid", "error", err)
		return nil, false
	}

	sendUserMessages := cfg.SendUserMessages == nil || *cfg.SendUserMessages
	client := &assessor.Client{
		URL:        cfg.URL,
		APIKey:     ta.APIKey,
		AuthScheme: cmp.Or(cfg.AuthScheme, config.AssessorAuthAPIKey),
		Model:      cfg.Model,
		HTTP:       &http.Client{},
		Backoff:    []time.Duration{250 * time.Millisecond},
	}
	opts := permission.AssessorOptions{
		Assessor:           assessor.New(client, th, sendUserMessages),
		Mode:               permission.AssessorMode(cmp.Or(cfg.Mode, config.AssessorModeOff)),
		Timeout:            time.Duration(cmp.Or(cfg.TimeoutSeconds, defaultAssessorTimeoutSeconds)) * time.Second,
		ExplicitAskToHuman: cfg.ExplicitAsk == config.AssessorExplicitAskHuman,
	}
	if sendUserMessages {
		opts.Intent = &intentSource{sessions: sessions, messages: messages}
	}
	return permission.WithAssessor(opts), true
}

func assessorThresholds(cfg *config.PermissionAssessor) assessor.Thresholds {
	th := assessor.DefaultThresholds()
	if cfg.EscalateAt != nil {
		th.EscalateAt = *cfg.EscalateAt
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
		if text := path[i].Content().Text; text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) > n {
		texts = texts[len(texts)-n:]
	}
	return texts, nil
}

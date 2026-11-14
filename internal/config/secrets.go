package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"

	"github.com/Broderick-Westrope/anvil/internal/secrets"
)

// Secrets configures masking of credentials in everything sent to models.
// It is only read from user-level config files, so a repository can't turn
// masking off or allow its own values through.
type Secrets struct {
	Enabled *bool    `json:"enabled,omitempty" jsonschema:"description=Replace credentials with placeholders in every request to a model. Needs a restart,default=true"`
	Allow   []string `json:"allow,omitempty" jsonschema:"description=Values never masked\\, such as sample keys in fixtures. An entry starting with re: is a regular expression"`
}

// MaskingEnabled reports whether masking is on. It is on unless a
// user-level config file turns it off.
func (s *Secrets) MaskingEnabled() bool {
	return s == nil || s.Enabled == nil || *s.Enabled
}

// AllowList returns the values never masked.
func (s *Secrets) AllowList() []string {
	if s == nil {
		return nil
	}
	return s.Allow
}

// loadTrustedSecrets merges the secrets blocks from the trusted paths. It
// returns nil when none sets the block.
func loadTrustedSecrets(paths []string) (*Secrets, error) {
	merged, err := loadTrustedBlock(paths, "secrets")
	if err != nil || merged == nil {
		return nil, err
	}
	var s Secrets
	err = json.Unmarshal(merged, &s)
	if err == nil {
		err = s.Validate()
	}
	if err != nil {
		return nil, fmt.Errorf("invalid secrets: %w", err)
	}
	return &s, nil
}

// Validate rejects allow entries whose regular expression doesn't compile,
// so a fixture the user meant to allow isn't silently masked.
func (s *Secrets) Validate() error {
	return secrets.CheckAllow(s.AllowList())
}

// applyTrustedSecrets replaces whatever secrets block the full merge
// produced with the trusted one, warning when a non-trusted file tried to
// change it.
func (c *Config) applyTrustedSecrets(trusted *Secrets) {
	if !reflect.DeepEqual(c.Secrets, trusted) {
		slog.Warn("Ignoring secrets from project config; it is only read from user-level config")
	}
	c.Secrets = trusted
}

// SecretsAtStartup returns the secrets settings as loaded at startup.
// Masking reads these rather than the live config, so the models and the
// system prompt never disagree; a change applies after a restart.
func (s *ConfigStore) SecretsAtStartup() *Secrets {
	cfg, _, _ := s.StartupSnapshot()
	if cfg == nil {
		cfg = s.Config()
	}
	return cfg.Secrets
}

// secretsChanged reports whether the masking settings differ.
func secretsChanged(a, b *Config) bool {
	return a.Secrets.MaskingEnabled() != b.Secrets.MaskingEnabled() || !slices.Equal(a.Secrets.AllowList(), b.Secrets.AllowList())
}

// ObserveProviderCredentials sets fn to hear each provider credential set
// from now on, such as a login or a refreshed OAuth token.
func (s *ConfigStore) ObserveProviderCredentials(fn func(ProviderConfig)) {
	s.credentialObserver.Store(&fn)
}

// Credentials returns the provider's credentials for secret masking: its
// resolved API key (also without an auth scheme, as OAuth providers store
// "Bearer <token>"), its OAuth tokens and the credentials among its extra
// headers.
func (c ProviderConfig) Credentials(apiKey string) []secrets.Known {
	kind := c.ID + "_api_key"
	known := []secrets.Known{{Kind: kind, Value: apiKey}, {Kind: kind, Value: secrets.WithoutAuthScheme(apiKey)}}
	if token := c.OAuthToken; token != nil {
		known = append(known, secrets.Known{Kind: kind, Value: token.AccessToken}, secrets.Known{Kind: kind, Value: token.RefreshToken})
	}
	return append(known, secrets.HeaderKnown(kind, c.ExtraHeaders)...)
}

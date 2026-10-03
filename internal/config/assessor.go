package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"regexp"

	"github.com/qjebbs/go-jsons"
	"github.com/tidwall/gjson"
)

// DefaultAssessorAPIKeyEnv is the environment variable the assessor API key
// is read from when permission_assessor.api_key_env is not set.
const DefaultAssessorAPIKeyEnv = "BASETEN_API_KEY"

// Assessor modes.
const (
	AssessorModeOff     = "off"
	AssessorModeShadow  = "shadow"
	AssessorModeEnforce = "enforce"
)

// Assessor auth schemes.
const (
	AssessorAuthAPIKey = "Api-Key"
	AssessorAuthBearer = "Bearer"
)

// Assessor explicit_ask routing values.
const (
	AssessorExplicitAskAssessor = "assessor"
	AssessorExplicitAskHuman    = "human"
)

const maxAssessorTimeoutSeconds = 60

var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PermissionAssessor configures the classifier that answers permission
// prompts on the user's behalf. It is only honoured from user-level config
// files; see [ConfigStore.TrustedAssessor].
type PermissionAssessor struct {
	Mode  string `json:"mode,omitempty" jsonschema:"enum=off,enum=shadow,enum=enforce,default=off"`
	URL   string `json:"url,omitempty" jsonschema:"description=Full System One endpoint URL (https only)"`
	Model string `json:"model,omitempty" jsonschema:"example=von-1.0.0"`
	// APIKeyEnv names the environment variable holding the API key, so
	// users whose key lives under a different name don't have to rename
	// it. Optional; defaults to BASETEN_API_KEY.
	APIKeyEnv        string   `json:"api_key_env,omitempty" jsonschema:"description=Name of the environment variable that holds the API key,default=BASETEN_API_KEY,example=BASETEN_API_KEY,example=TYPESAFE_API_KEY"`
	AuthScheme       string   `json:"auth_scheme,omitempty" jsonschema:"enum=Api-Key,enum=Bearer,default=Api-Key"`
	TimeoutSeconds   int      `json:"timeout_seconds,omitempty" jsonschema:"default=8"`
	ExplicitAsk      string   `json:"explicit_ask,omitempty" jsonschema:"enum=assessor,enum=human,default=assessor"`
	SendUserMessages *bool    `json:"send_user_messages,omitempty" jsonschema:"default=true"`
	EscalateAt       *float64 `json:"escalate_at,omitempty"`
	DenyAt           *float64 `json:"deny_at,omitempty"`
	SeverityEscalate *float64 `json:"severity_escalate,omitempty"`
	UserRequestedAt  *float64 `json:"user_requested_at,omitempty"`
}

// TrustedAssessor is the resolved, trusted assessor config plus the
// API key captured before any project env was applied.
type TrustedAssessor struct {
	Config *PermissionAssessor
	APIKey string
}

// Validate rejects unknown enum values, non-https URLs, out-of-range or
// inconsistent thresholds, and out-of-range timeouts. Thresholds that are
// left unset are not cross-checked here; the consumer validates them again
// after overlaying its defaults.
func (p *PermissionAssessor) Validate() error {
	if p == nil {
		return nil
	}
	var errs []error
	switch p.Mode {
	case "", AssessorModeOff, AssessorModeShadow, AssessorModeEnforce:
	default:
		errs = append(errs, fmt.Errorf("mode %q must be one of off, shadow, enforce", p.Mode))
	}
	switch p.AuthScheme {
	case "", AssessorAuthAPIKey, AssessorAuthBearer:
	default:
		errs = append(errs, fmt.Errorf("auth_scheme %q must be one of Api-Key, Bearer", p.AuthScheme))
	}
	switch p.ExplicitAsk {
	case "", AssessorExplicitAskAssessor, AssessorExplicitAskHuman:
	default:
		errs = append(errs, fmt.Errorf("explicit_ask %q must be one of assessor, human", p.ExplicitAsk))
	}
	if p.URL != "" {
		u, err := url.Parse(p.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("url %q must be an absolute https URL", p.URL))
		}
	}
	if p.APIKeyEnv != "" && !envVarNameRe.MatchString(p.APIKeyEnv) {
		errs = append(errs, fmt.Errorf("api_key_env %q is not a valid environment variable name", p.APIKeyEnv))
	}
	if p.TimeoutSeconds < 0 || p.TimeoutSeconds > maxAssessorTimeoutSeconds {
		errs = append(errs, fmt.Errorf("timeout_seconds %d must be between 0 and %d", p.TimeoutSeconds, maxAssessorTimeoutSeconds))
	}
	errs = append(errs,
		checkAssessorRange("escalate_at", p.EscalateAt, 1),
		checkAssessorRange("deny_at", p.DenyAt, 1),
		checkAssessorRange("user_requested_at", p.UserRequestedAt, 1),
		checkAssessorRange("severity_escalate", p.SeverityEscalate, 3),
	)
	if p.EscalateAt != nil && p.DenyAt != nil && *p.EscalateAt >= *p.DenyAt {
		errs = append(errs, fmt.Errorf("escalate_at (%v) must be less than deny_at (%v)", *p.EscalateAt, *p.DenyAt))
	}
	return errors.Join(errs...)
}

func checkAssessorRange(name string, v *float64, upper float64) error {
	if v == nil {
		return nil
	}
	if *v < 0 || *v > upper {
		return fmt.Errorf("%s (%v) must be between 0 and %v", name, *v, upper)
	}
	return nil
}

// trustedConfigPaths returns the user-level config files the assessor block
// may be read from. It reads the process env, so it must only be called
// before any config-provided env has been applied.
func trustedConfigPaths() []string {
	return []string{systemConfigPath, GlobalConfig(), GlobalConfigData()}
}

// loadAssessorBlock merges the permission_assessor blocks from paths and
// validates the result. It returns nil when no path sets the block.
func loadAssessorBlock(paths []string) (*PermissionAssessor, error) {
	var blocks [][]byte
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to open config file %s: %w", path, err)
		}
		if len(data) == 0 {
			continue
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid JSON in config file %s", path)
		}
		block := gjson.GetBytes(data, "permission_assessor")
		switch {
		case !block.Exists() || block.Type == gjson.Null:
			continue
		case !block.IsObject():
			return nil, fmt.Errorf("permission_assessor in %s must be an object", path)
		}
		blocks = append(blocks, []byte(block.Raw))
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	merged, err := jsons.Merge(blocks)
	if err != nil {
		return nil, fmt.Errorf("failed to merge permission_assessor: %w", err)
	}
	var pa PermissionAssessor
	if err := json.Unmarshal(merged, &pa); err != nil {
		return nil, fmt.Errorf("invalid permission_assessor: %w", err)
	}
	if err := pa.Validate(); err != nil {
		return nil, fmt.Errorf("invalid permission_assessor: %w", err)
	}
	return &pa, nil
}

// loadTrustedAssessor reads the assessor block from the trusted paths and
// captures its API key from the current process env. Callers must invoke it
// before any config-provided env is applied.
func loadTrustedAssessor(paths []string) (*TrustedAssessor, error) {
	block, err := loadAssessorBlock(paths)
	if err != nil || block == nil {
		return nil, err
	}
	return &TrustedAssessor{
		Config: block,
		APIKey: os.Getenv(cmp.Or(block.APIKeyEnv, DefaultAssessorAPIKeyEnv)),
	}, nil
}

// applyTrustedAssessor replaces whatever permission_assessor the full merge
// produced with the trusted one, warning when a non-trusted file tried to
// change it.
func (c *Config) applyTrustedAssessor(ta *TrustedAssessor) {
	var trusted *PermissionAssessor
	if ta != nil {
		trusted = ta.Config
	}
	if !reflect.DeepEqual(c.PermissionAssessor, trusted) {
		slog.Warn("Ignoring permission_assessor from project config; it is only read from user-level config")
	}
	c.PermissionAssessor = trusted
}

// TrustedAssessor returns the assessor config read only from user-level
// config files, with the API key captured at initial load. It returns nil
// when no user-level file configures the assessor.
func (s *ConfigStore) TrustedAssessor() *TrustedAssessor {
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	return s.trustedAssessor
}

func (s *ConfigStore) setTrustedAssessor(ta *TrustedAssessor) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	s.trustedAssessor = ta
}

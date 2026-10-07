package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/qjebbs/go-jsons"
	"github.com/tidwall/gjson"
)

// DefaultBouncerAPIKeyEnv is the environment variable the bouncer API key
// is read from when bouncer.api_key_env is not set.
const DefaultBouncerAPIKeyEnv = "BASETEN_API_KEY"

// BouncerMode controls whether the bouncer runs and whether
// its verdict is acted on.
type BouncerMode string

const (
	// BouncerOff disables the bouncer.
	BouncerOff BouncerMode = "off"
	// BouncerShadow assesses and logs, but the human still decides.
	BouncerShadow BouncerMode = "shadow"
	// BouncerEnforce acts on the bouncer's allow and deny verdicts.
	BouncerEnforce BouncerMode = "enforce"
)

// BouncerAuthScheme is the Authorization header scheme sent to the
// bouncer endpoint.
type BouncerAuthScheme string

const (
	// BouncerAuthAPIKey sends "Api-Key <key>", as Baseten expects.
	BouncerAuthAPIKey BouncerAuthScheme = "Api-Key"
	// BouncerAuthBearer sends "Bearer <key>", as TypeSafe expects.
	BouncerAuthBearer BouncerAuthScheme = "Bearer"
)

// BouncerExplicitAsk decides who answers requests that an explicit "ask"
// rule matched.
type BouncerExplicitAsk string

const (
	// BouncerExplicitAskBouncer lets the bouncer answer them first.
	BouncerExplicitAskBouncer BouncerExplicitAsk = "bouncer"
	// BouncerExplicitAskHuman always sends them to the human.
	BouncerExplicitAskHuman BouncerExplicitAsk = "human"
)

// BouncerReviewMode controls the small-model reviewer that gives a second
// opinion when the bouncer sends a request to the human.
type BouncerReviewMode string

const (
	// BouncerReviewOff disables the reviewer.
	BouncerReviewOff BouncerReviewMode = "off"
	// BouncerReviewShadow shows and logs the reviewer's opinion without
	// acting on it.
	BouncerReviewShadow BouncerReviewMode = "shadow"
)

const maxBouncerTimeoutSeconds = 60

// BouncerHazardAxes are the hazard axes escalate_at_axes may set. They
// must match the bouncer's hazard questions.
var BouncerHazardAxes = []string{"destructive", "exfiltration", "credentials", "remote_exec", "shared_infra"}

var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Bouncer configures the classifier that answers permission
// prompts on the user's behalf. It is only honoured from user-level config
// files; see [ConfigStore.TrustedBouncer].
type Bouncer struct {
	Mode  BouncerMode `json:"mode,omitempty" jsonschema:"enum=off,enum=shadow,enum=enforce,default=off"`
	URL   string      `json:"url,omitempty" jsonschema:"description=Full System One endpoint URL (https only)"`
	Model string      `json:"model,omitempty" jsonschema:"example=von-1.0.0"`
	// APIKeyEnv names the environment variable holding the API key, so
	// users whose key lives under a different name don't have to rename
	// it. Optional; defaults to BASETEN_API_KEY.
	APIKeyEnv        string             `json:"api_key_env,omitempty" jsonschema:"description=Name of the environment variable that holds the API key,default=BASETEN_API_KEY,example=BASETEN_API_KEY,example=TYPESAFE_API_KEY"`
	AuthScheme       BouncerAuthScheme  `json:"auth_scheme,omitempty" jsonschema:"enum=Api-Key,enum=Bearer,default=Api-Key"`
	TimeoutSeconds   int                `json:"timeout_seconds,omitempty" jsonschema:"description=Bouncer call timeout in seconds; 0 uses the default,minimum=0,maximum=60,default=8"`
	ExplicitAsk      BouncerExplicitAsk `json:"explicit_ask,omitempty" jsonschema:"enum=bouncer,enum=human,default=bouncer"`
	SendUserMessages *bool              `json:"send_user_messages,omitempty" jsonschema:"default=true"`
	Review           BouncerReviewMode  `json:"review,omitempty" jsonschema:"description=Whether the small model reviews requests the bouncer sends to you against your recent messages. shadow shows and logs its opinion without acting on it,enum=off,enum=shadow,default=shadow"`
	EscalateAt       *float64           `json:"escalate_at,omitempty" jsonschema:"description=Hazard probability at or above which the request goes to the human whatever its severity. Setting it applies one value to every axis; escalate_at_axes overrides it per axis. When unset each axis uses its own default (destructive 0.5; the other axes 0.6),minimum=0,maximum=1"`
	// EscalateAtAxes overrides EscalateAt for individual hazard axes.
	EscalateAtAxes   map[string]float64 `json:"escalate_at_axes,omitempty" jsonschema:"description=Per-axis overrides of escalate_at keyed by destructive or exfiltration or credentials or remote_exec or shared_infra"`
	ConcernAt        *float64           `json:"concern_at,omitempty" jsonschema:"description=Hazard probability at or above which the request goes to the human when severity also reaches severity_concern,minimum=0,maximum=1,default=0.35"`
	SeverityConcern  *float64           `json:"severity_concern,omitempty" jsonschema:"description=Severity score (0-3) at or above which a hazard in the concern band goes to the human,minimum=0,maximum=3,default=1.5"`
	DenyAt           *float64           `json:"deny_at,omitempty" jsonschema:"description=Hazard probability at or above which the request is flagged as a deny when severity also reaches severity_deny and the user did not ask for it,minimum=0,maximum=1,default=0.9"`
	SeverityDeny     *float64           `json:"severity_deny,omitempty" jsonschema:"description=Severity score (0-3) a request must also reach before a hazard at deny_at flags it as a deny; below it the request is only escalated,minimum=0,maximum=3,default=2"`
	SeverityEscalate *float64           `json:"severity_escalate,omitempty" jsonschema:"description=Severity score (0-3) at or above which the request goes to the human,minimum=0,maximum=3,default=2"`
	UserRequestedAt  *float64           `json:"user_requested_at,omitempty" jsonschema:"description=User-requested probability at or above which a likely deny goes to the human instead,minimum=0,maximum=1,default=0.7"`
}

// TrustedBouncer is the resolved, trusted bouncer config plus the
// API key captured before any project env was applied.
type TrustedBouncer struct {
	Config *Bouncer
	APIKey string
}

// Validate rejects unknown enum values, non-https URLs, out-of-range or
// inconsistent thresholds, and out-of-range timeouts. Thresholds that are
// left unset are not cross-checked here; the consumer validates them again
// after overlaying its defaults.
func (p *Bouncer) Validate() error {
	if p == nil {
		return nil
	}
	var errs []error
	switch p.Mode {
	case "", BouncerOff, BouncerShadow, BouncerEnforce:
	default:
		errs = append(errs, fmt.Errorf("mode %q must be one of off, shadow, enforce", p.Mode))
	}
	switch p.Review {
	case "", BouncerReviewOff, BouncerReviewShadow:
	default:
		errs = append(errs, fmt.Errorf("review %q must be one of off, shadow", p.Review))
	}
	switch p.AuthScheme {
	case "", BouncerAuthAPIKey, BouncerAuthBearer:
	default:
		errs = append(errs, fmt.Errorf("auth_scheme %q must be one of Api-Key, Bearer", p.AuthScheme))
	}
	switch p.ExplicitAsk {
	case "", BouncerExplicitAskBouncer, BouncerExplicitAskHuman:
	default:
		errs = append(errs, fmt.Errorf("explicit_ask %q must be one of bouncer, human", p.ExplicitAsk))
	}
	if p.URL != "" {
		u, err := url.Parse(p.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("url %q must be an absolute https URL", p.URL))
		}
	}
	if p.APIKeyEnv != "" && !envVarNameRe.MatchString(p.APIKeyEnv) {
		// The value isn't echoed: an invalid name is often the key itself
		// pasted into the wrong field, and this error is logged.
		errs = append(errs, errors.New("api_key_env must be the name of an environment variable (letters, digits, and underscores), not the key itself"))
	}
	if p.TimeoutSeconds < 0 || p.TimeoutSeconds > maxBouncerTimeoutSeconds {
		errs = append(errs, fmt.Errorf("timeout_seconds %d must be between 0 and %d", p.TimeoutSeconds, maxBouncerTimeoutSeconds))
	}
	errs = append(errs,
		checkBouncerRange("escalate_at", p.EscalateAt, 1),
		checkBouncerRange("deny_at", p.DenyAt, 1),
		checkBouncerRange("user_requested_at", p.UserRequestedAt, 1),
		checkBouncerRange("severity_escalate", p.SeverityEscalate, 3),
		checkBouncerRange("concern_at", p.ConcernAt, 1),
		checkBouncerRange("severity_concern", p.SeverityConcern, 3),
		checkBouncerRange("severity_deny", p.SeverityDeny, 3),
	)
	if p.EscalateAt != nil && p.DenyAt != nil && *p.EscalateAt >= *p.DenyAt {
		errs = append(errs, fmt.Errorf("escalate_at (%v) must be less than deny_at (%v)", *p.EscalateAt, *p.DenyAt))
	}
	for _, axis := range slices.Sorted(maps.Keys(p.EscalateAtAxes)) {
		v := p.EscalateAtAxes[axis]
		if !slices.Contains(BouncerHazardAxes, axis) {
			errs = append(errs, fmt.Errorf("escalate_at_axes has unknown axis %q; valid axes are %s", axis, strings.Join(BouncerHazardAxes, ", ")))
			continue
		}
		errs = append(errs, checkBouncerRange("escalate_at_axes."+axis, &v, 1))
		if p.DenyAt != nil && v >= *p.DenyAt {
			errs = append(errs, fmt.Errorf("escalate_at_axes.%s (%v) must be less than deny_at (%v)", axis, v, *p.DenyAt))
		}
	}
	return errors.Join(errs...)
}

func checkBouncerRange(name string, v *float64, upper float64) error {
	if v == nil {
		return nil
	}
	if *v < 0 || *v > upper {
		return fmt.Errorf("%s (%v) must be between 0 and %v", name, *v, upper)
	}
	return nil
}

// trustedConfigPaths returns the user-level config files the bouncer block
// may be read from. It reads the process env, so it must only be called
// before any config-provided env has been applied.
func trustedConfigPaths() []string {
	return []string{systemConfigPath, GlobalConfigData(), GlobalConfig()}
}

// loadBouncerBlock merges the bouncer blocks from paths and
// validates the result. It returns nil when no path sets the block.
func loadBouncerBlock(paths []string) (*Bouncer, error) {
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
		block := gjson.GetBytes(data, "bouncer")
		switch {
		case !block.Exists() || block.Type == gjson.Null:
			continue
		case !block.IsObject():
			return nil, fmt.Errorf("bouncer in %s must be an object", path)
		}
		blocks = append(blocks, []byte(block.Raw))
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	merged, err := jsons.Merge(blocks)
	if err != nil {
		return nil, fmt.Errorf("failed to merge bouncer: %w", err)
	}
	var pa Bouncer
	if err := json.Unmarshal(merged, &pa); err != nil {
		return nil, fmt.Errorf("invalid bouncer: %w", err)
	}
	if err := pa.Validate(); err != nil {
		return nil, fmt.Errorf("invalid bouncer: %w", err)
	}
	return &pa, nil
}

// loadTrustedBouncer reads the bouncer block from the trusted paths and
// captures its API key from the current process env. Callers must invoke it
// before any config-provided env is applied.
func loadTrustedBouncer(paths []string) (*TrustedBouncer, error) {
	block, err := loadBouncerBlock(paths)
	if err != nil || block == nil {
		return nil, err
	}
	return &TrustedBouncer{
		Config: block,
		APIKey: os.Getenv(cmp.Or(block.APIKeyEnv, DefaultBouncerAPIKeyEnv)),
	}, nil
}

// applyTrustedBouncer replaces whatever bouncer the full merge
// produced with the trusted one, warning when a non-trusted file tried to
// change it.
func (c *Config) applyTrustedBouncer(ta *TrustedBouncer) {
	var trusted *Bouncer
	if ta != nil {
		trusted = ta.Config
	}
	if !reflect.DeepEqual(c.Bouncer, trusted) {
		slog.Warn("Ignoring bouncer from project config; it is only read from user-level config")
	}
	c.Bouncer = trusted
}

// TrustedBouncer returns the bouncer config read only from user-level
// config files, with the API key captured at initial load. It returns nil
// when no user-level file configures the bouncer.
func (s *ConfigStore) TrustedBouncer() *TrustedBouncer {
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	return s.trustedBouncer
}

func (s *ConfigStore) setTrustedBouncer(ta *TrustedBouncer) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	s.trustedBouncer = ta
}

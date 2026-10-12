package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApplyOverrides_Characterization pins how user agent config is laid
// over the default agents and which agents are removed as disabled.
func TestApplyOverrides_Characterization(t *testing.T) {
	t.Parallel()

	fixer := func() Agent {
		return Agent{
			ID:              "fixer",
			Name:            "Fixer",
			Description:     "Fixes things",
			Model:           "anthropic/big",
			Variant:         "va",
			ReasoningEffort: "low",
			Think:           ptr(false),
			AllowedTools:    []string{"bash"},
			AllowedSkills:   []string{"go-style"},
			AllowedMCP:      map[string][]string{"muninn": nil},
			AppendPrompt:    "be careful",
		}
	}

	// Many agents on each side of the user-only branch, so stopping the
	// loop early misses some whatever order the map yields them in.
	mixedDefaults := map[string]Agent{}
	mixedUser := map[string]Agent{}
	mixedWant := map[string]Agent{}
	for i := range 8 {
		defaultName := fmt.Sprintf("default-%d", i)
		mixedDefaults[defaultName] = Agent{ID: defaultName, Model: "anthropic/big"}
		mixedUser[defaultName] = Agent{Model: "openai/other"}
		mixedWant[defaultName] = Agent{ID: defaultName, Model: "openai/other"}

		userName := fmt.Sprintf("user-%d", i)
		mixedUser[userName] = Agent{Model: "openai/small"}
		mixedWant[userName] = Agent{ID: userName, Name: userName, Model: "openai/small"}
	}

	tests := map[string]struct {
		defaults map[string]Agent
		user     map[string]Agent
		disabled []string
		want     map[string]Agent
	}{
		"user agent missing from the defaults is added with its key as id and name": {
			defaults: map[string]Agent{"fixer": fixer()},
			user:     map[string]Agent{"scout": {Model: "openai/small"}},
			want: map[string]Agent{
				"fixer": fixer(),
				"scout": {ID: "scout", Name: "scout", Model: "openai/small"},
			},
		},
		"every user agent is applied whether or not it is a default": {
			defaults: mixedDefaults,
			user:     mixedUser,
			want:     mixedWant,
		},
		"user agent missing from the defaults keeps its own id and name": {
			defaults: map[string]Agent{},
			user:     map[string]Agent{"scout": {ID: "own-id", Name: "Own"}},
			want:     map[string]Agent{"scout": {ID: "own-id", Name: "Own"}},
		},
		"every overridable field the user sets replaces the default": {
			defaults: map[string]Agent{"fixer": fixer()},
			user: map[string]Agent{"fixer": {
				ID:              "ignored",
				Name:            "Ignored",
				Description:     "Ignored",
				Model:           "openai/other",
				Variant:         "vb",
				ReasoningEffort: "high",
				Think:           ptr(true),
				AllowedTools:    []string{},
				AllowedSkills:   []string{},
				AllowedMCP:      map[string][]string{},
				AppendPrompt:    "be quick",
			}},
			want: map[string]Agent{"fixer": {
				ID:              "fixer",
				Name:            "Fixer",
				Description:     "Fixes things",
				Model:           "openai/other",
				Variant:         "vb",
				ReasoningEffort: "high",
				Think:           ptr(true),
				AllowedTools:    []string{},
				AllowedSkills:   []string{},
				AllowedMCP:      map[string][]string{},
				AppendPrompt:    "be quick",
			}},
		},
		"user can turn off thinking the default turns on": {
			defaults: map[string]Agent{"fixer": {ID: "fixer", Think: ptr(true)}},
			user:     map[string]Agent{"fixer": {Think: ptr(false)}},
			want:     map[string]Agent{"fixer": {ID: "fixer", Think: ptr(false)}},
		},
		"fields the user leaves unset keep the default": {
			defaults: map[string]Agent{"fixer": fixer()},
			user:     map[string]Agent{"fixer": {}},
			want:     map[string]Agent{"fixer": fixer()},
		},
		"user can disable a default agent": {
			defaults: map[string]Agent{"fixer": fixer()},
			user:     map[string]Agent{"fixer": {Disabled: true}},
			want:     map[string]Agent{},
		},
		"default agent that is disabled stays removed when the user overrides it": {
			defaults: map[string]Agent{"fixer": {ID: "fixer", Disabled: true}},
			user:     map[string]Agent{"fixer": {Model: "openai/other"}},
			want:     map[string]Agent{},
		},
		"disabled user agent is not added": {
			defaults: map[string]Agent{"fixer": fixer()},
			user:     map[string]Agent{"scout": {Disabled: true}},
			want:     map[string]Agent{"fixer": fixer()},
		},
		"disabled agents list removes default and user agents": {
			defaults: map[string]Agent{"fixer": fixer(), "keep": {ID: "keep"}},
			user:     map[string]Agent{"scout": {}},
			disabled: []string{"fixer", "scout", "absent"},
			want:     map[string]Agent{"keep": {ID: "keep"}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := applyOverrides(tc.defaults, tc.user, tc.disabled)
			require.Equal(t, tc.want, got)
		})
	}
}

package dialog

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/require"
)

func TestSkillPickerItemSourceLabel(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		skill *skills.Skill
		want  string
	}{
		"builtin":          {skill: &skills.Skill{Source: skills.SourceBuiltin}, want: "builtin"},
		"user":             {skill: &skills.Skill{}, want: "user"},
		"plugin":           {skill: &skills.Skill{Source: "plugin:ce"}, want: "plugin:ce"},
		"unlisted user":    {skill: &skills.Skill{Unlisted: true}, want: "unlisted · user"},
		"unlisted plugin":  {skill: &skills.Skill{Source: "plugin:ce", Unlisted: true}, want: "unlisted · plugin:ce"},
		"unlisted builtin": {skill: &skills.Skill{Source: skills.SourceBuiltin, Unlisted: true}, want: "unlisted · builtin"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := &SkillPickerItem{skill: tc.skill}
			require.Equal(t, tc.want, item.sourceLabel())
		})
	}
}

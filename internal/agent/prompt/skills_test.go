package prompt

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/require"
)

func TestPromptBuildUsesProvidedAvailableSkills(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	store, err := config.Init(workingDir, "", false)
	require.NoError(t, err)

	p, err := NewPrompt("test", "{{.AvailSkillXML}}", WithAvailableSkills([]*skills.Skill{
		{
			Name:          "plugin-skill",
			Description:   "From a plugin",
			SkillFilePath: "/tmp/plugin/skills/plugin-skill/SKILL.md",
			Source:        "plugin:demo",
		},
	}))
	require.NoError(t, err)

	built, err := p.Build(context.Background(), "test-provider", "test-model", store)
	require.NoError(t, err)
	require.Contains(t, built, "<name>plugin-skill</name>")
	require.False(t, strings.Contains(built, "<type>builtin</type>"))
}

func TestSkillsUsageAvailability(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile("../templates/base.md.tpl")
	require.NoError(t, err)
	for _, catalog := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			t.Run(fmt.Sprintf("catalog=%t/view=%t", catalog, available), func(t *testing.T) {
				t.Parallel()
				store, err := config.Init(t.TempDir(), t.TempDir(), false)
				require.NoError(t, err)
				store.Config().Options.ContextPaths = nil
				var skillsList []*skills.Skill
				if catalog {
					skillsList = []*skills.Skill{{Name: "example", Description: "Example skill", SkillFilePath: "/private/example/SKILL.md"}}
				}
				p, err := NewPrompt("test", string(base)+`{{template "skills_and_context" .}}`, WithAvailableSkills(skillsList), WithViewToolAvailable(available))
				require.NoError(t, err)
				built, err := p.Build(t.Context(), "test", "test", store)
				require.NoError(t, err)
				require.Equal(t, catalog, strings.Contains(built, "</available_skills>"))
				require.Equal(t, available, strings.Contains(built, "<skills_usage>"))
				require.NotContains(t, built, "<location>")
				require.NotContains(t, built, "/private/example")
				require.Contains(t, built, "A skill supplies context for the task you were given.")
				require.Contains(t, built, "never authorizes delegation, commits, pushes or pull requests")
				if available {
					require.Contains(t, built, "`skill_name` set to the exact `<name>` (case sensitive)")
					require.Contains(t, built, "not listed above")
					require.Contains(t, built, "current registry snapshot")
					require.Contains(t, built, "do not search the filesystem")
					require.Contains(t, built, "directory of the returned location")
					require.Contains(t, built, "do not try to execute their scripts")
				}
			})
		}
	}
}

func TestSkillsViewAvailableByDefault(t *testing.T) {
	t.Parallel()
	store, err := config.Init(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	p, err := NewPrompt("test", "{{.HasViewTool}}", WithAvailableSkills(nil))
	require.NoError(t, err)
	built, err := p.Build(t.Context(), "test", "test", store)
	require.NoError(t, err)
	require.Equal(t, "true", built)
}

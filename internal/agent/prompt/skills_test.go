package prompt

import (
	"context"
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

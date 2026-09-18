package agent

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

func TestOrchestratorPromptGoldenFile(t *testing.T) {
	t.Parallel()

	fixedTime := func() time.Time {
		ts, _ := time.Parse("1/2/2006", "1/1/2025")
		return ts
	}

	agentsBlock := "<Agents>\n\n" +
		"@explorer\n" +
		"- Role: Fast codebase search specialist.\n" +
		"- Delegate when: Broad discovery needed.\n" +
		"- Don't delegate when: Exact file path is known.\n" +
		"\n" +
		"@fixer\n" +
		"- Role: Bounded implementation specialist.\n" +
		"- Delegate when: Well-defined implementation work.\n" +
		"- Don't delegate when: Task needs research.\n" +
		"\n" +
		"</Agents>"

	delegationWorkflow := "<Workflow>\nTest workflow content.\n</Workflow>"

	p, err := orchestratorPrompt(
		prompt.WithTimeFunc(fixedTime),
		prompt.WithPlatform("linux"),
		prompt.WithWorkingDir("/project"),
		prompt.WithAgentsBlock(agentsBlock),
		prompt.WithDelegationWorkflow(delegationWorkflow),
	)
	require.NoError(t, err)

	// Use a temp dir for config so no real context files are discovered.
	tmpDir := t.TempDir()
	cfg, err := config.Init(tmpDir, t.TempDir(), false)
	require.NoError(t, err)

	// Clear paths that would introduce non-deterministic content.
	cfg.Config().Options.SkillsPaths = nil
	cfg.Config().Options.ContextPaths = nil
	cfg.Config().LSP = nil

	result, err := p.Build(context.Background(), "test-provider", "test-model", cfg)
	require.NoError(t, err)

	golden.RequireEqual(t, []byte(result))
}

func TestSkillsRenderedPrompts(t *testing.T) {
	t.Parallel()
	for name, constructor := range map[string]func(...prompt.Option) (*prompt.Prompt, error){"orchestrator": orchestratorPrompt, "specialist": specialistPrompt} {
		for _, available := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/view=%t", name, available), func(t *testing.T) {
				t.Parallel()
				cfg, err := config.Init(t.TempDir(), t.TempDir(), false)
				require.NoError(t, err)
				cfg.Config().Options.ContextPaths = nil
				p, err := constructor(prompt.WithAvailableSkills(nil), prompt.WithViewToolAvailable(available))
				require.NoError(t, err)
				built, err := p.Build(t.Context(), "test", "test", cfg)
				require.NoError(t, err)
				require.Equal(t, available, strings.Contains(built, "<skills_usage>"))
				require.Equal(t, available && name == "orchestrator", strings.Contains(built, "LOAD MATCHING SKILLS"))
				require.NotContains(t, built, "<location>")
				require.Contains(t, built, "never grants you tools you do not have")
				if available {
					require.Contains(t, built, "skill_name")
				}
				if name == "orchestrator" {
					rules := strings.Split(strings.Split(built, "<critical_rules>")[1], "</critical_rules>")[0]
					numbers := regexp.MustCompile(`(?m)^(\d+)\. `).FindAllStringSubmatch(rules, -1)
					count := 14
					if available {
						count = 15
						require.Contains(t, rules, "15. **LOAD MATCHING SKILLS**")
						require.Contains(t, rules, "`skill_name`")
					}
					require.Len(t, numbers, count)
					for i, number := range numbers {
						require.Equal(t, strconv.Itoa(i+1), number[1])
					}
					require.Contains(t, rules, "14. **LIMIT FILE READS**")
				} else {
					require.NotContains(t, built, "<critical_rules>")
				}
			})
		}
	}
}

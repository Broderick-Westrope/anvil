package agent

import (
	"context"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
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

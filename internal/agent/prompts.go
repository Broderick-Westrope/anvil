package agent

import (
	"context"
	_ "embed"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/coder.md.tpl
var coderPromptTmpl []byte

//go:embed templates/task.md.tpl
var taskPromptTmpl []byte

//go:embed templates/initialize.md.tpl
var initializePromptTmpl []byte

//go:embed templates/base.md.tpl
var basePromptTmpl []byte

//go:embed templates/orchestrator.md.tpl
var orchestratorPromptTmpl []byte

//go:embed templates/specialist.md.tpl
var specialistPromptTmpl []byte

// Deprecated: Use orchestratorPrompt instead.
func coderPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("coder", string(coderPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

// Deprecated: Use specialistPrompt instead.
func taskPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("task", string(taskPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func orchestratorPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	combined := string(basePromptTmpl) + "\n" + string(orchestratorPromptTmpl)
	systemPrompt, err := prompt.NewPrompt("orchestrator", combined, opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func specialistPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	combined := string(basePromptTmpl) + "\n" + string(specialistPromptTmpl)
	systemPrompt, err := prompt.NewPrompt("specialist", combined, opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func InitializePrompt(cfg *config.ConfigStore) (string, error) {
	systemPrompt, err := prompt.NewPrompt("initialize", string(initializePromptTmpl))
	if err != nil {
		return "", err
	}
	return systemPrompt.Build(context.Background(), "", "", cfg)
}

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/home"
	"github.com/Broderick-Westrope/anvil/internal/plugin"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
	"github.com/spf13/cobra"
)

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Work with workflow scripts",
}

var workflowCheckCmd = &cobra.Command{
	Use:   "check <path>",
	Short: "Check a workflow script without running it",
	Long: `Parse a workflow script and check its requires() call without running any of its code.

<path> is a workflow script, or a command directory or COMMAND.md with a workflow: field.
Skills and agents are checked against what Anvil discovers with the current config.
Exits non-zero if the script doesn't parse or a requirement is missing.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		noCatalog, _ := cmd.Flags().GetBool("no-catalog")
		var cat workflow.Catalog
		if !noCatalog {
			cwd, err := ResolveCwd(cmd)
			if err != nil {
				return err
			}
			dataDir, _ := cmd.Flags().GetString("data-dir")
			store, err := config.Load(cwd, dataDir, false)
			if err != nil {
				return fmt.Errorf("load configuration: %w", err)
			}
			cat = workflowCatalog(store)
		}
		return checkWorkflow(cmd.OutOrStdout(), args[0], cat)
	},
}

func init() {
	workflowCheckCmd.Flags().Bool("no-catalog", false, "Skip loading config and checking skills and agents; check only parsing and references")
	workflowCmd.AddCommand(workflowCheckCmd)
}

var errWorkflowCheckFailed = errors.New("workflow check failed")

// checkWorkflow parses the workflow at path and writes its requirements and
// problems to w, one per line. It never runs the script. A nil cat skips
// the skill and agent checks.
func checkWorkflow(w io.Writer, path string, cat workflow.Catalog) error {
	scriptPath, problems := resolveWorkflowPath(path)
	if scriptPath == "" {
		for _, p := range problems {
			fmt.Fprintln(w, "error: "+p)
		}
		return errWorkflowCheckFailed
	}
	fmt.Fprintln(w, "workflow: "+scriptPath)

	script, err := workflow.Load(scriptPath)
	if err != nil {
		fmt.Fprintln(w, "error: "+err.Error())
		return errWorkflowCheckFailed
	}
	printRequirement(w, "skills", script.Requires.Skills)
	printRequirement(w, "agents", script.Requires.Agents)
	printRequirement(w, "references", script.Requires.References)
	for _, warning := range script.Warnings {
		fmt.Fprintln(w, "warning: "+warning)
	}

	if cat == nil {
		fmt.Fprintln(w, "note: skills and agents not checked (--no-catalog)")
		cat = allowAllCatalog{}
	}
	missing := script.Missing(cat)
	for _, m := range missing {
		fmt.Fprintln(w, "missing: "+m)
	}
	if len(missing) > 0 {
		return errWorkflowCheckFailed
	}
	return nil
}

// resolveWorkflowPath returns path itself if it is a script, or the script a command
// directory or COMMAND.md points at. It returns an empty path and the
// reasons when there's no script to check.
func resolveWorkflowPath(path string) (string, []string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", []string{err.Error()}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", []string{err.Error()}
	}
	commandFile := abs
	if info.IsDir() {
		commandFile = filepath.Join(abs, commands.CommandFileName)
	} else if filepath.Base(abs) != commands.CommandFileName {
		return abs, nil
	}

	cmd, err := commands.LoadCommand(commandFile)
	if err != nil {
		return "", []string{err.Error()}
	}
	if cmd.Workflow != "" {
		return cmd.Workflow, nil
	}
	if len(cmd.Warnings) > 0 {
		return "", cmd.Warnings
	}
	return "", []string{commandFile + " has no workflow: field"}
}

func printRequirement(w io.Writer, kind string, names []string) {
	if len(names) > 0 {
		fmt.Fprintf(w, "requires %s: %s\n", kind, strings.Join(names, ", "))
	}
}

type allowAllCatalog struct{}

func (allowAllCatalog) HasSkill(string) bool { return true }
func (allowAllCatalog) HasAgent(string) bool { return true }

// nameCatalog is a workflow.Catalog over fixed sets of names.
type nameCatalog struct {
	skills []string
	agents []string
}

func (c nameCatalog) HasSkill(name string) bool { return slices.Contains(c.skills, name) }
func (c nameCatalog) HasAgent(name string) bool { return slices.Contains(c.agents, name) }

// workflowCatalog lists the skills and agents a workflow could use with
// store's config, discovered the way the coordinator does at startup:
// active skills from the builtins, plugins and skills_paths, and agents
// from plugin agent files plus anvil.json, minus disabled ones.
func workflowCatalog(store *config.ConfigStore) workflow.Catalog {
	cfg := store.Config()
	plugins := plugin.DiscoverAll(cfg.Plugins, nil)

	discovered := skills.DiscoverBuiltin()
	agentDefaults := map[string]config.Agent{}
	for _, p := range plugins {
		if p.SkillsPath != "" {
			discovered = append(discovered, skills.Discover([]string{p.SkillsPath})...)
		}
		if p.AgentsPath != "" {
			for name := range pluginAgentNames(p.AgentsPath) {
				agentDefaults[name] = config.Agent{ID: name, Name: name}
			}
		}
	}
	if cfg.Options != nil {
		paths := make([]string, 0, len(cfg.Options.SkillsPaths))
		for _, pth := range cfg.Options.SkillsPaths {
			expanded := home.Long(pth)
			if strings.HasPrefix(expanded, "$") {
				if resolved, err := store.Resolver().ResolveValue(expanded); err == nil {
					expanded = resolved
				}
			}
			paths = append(paths, expanded)
		}
		discovered = append(discovered, skills.Discover(paths)...)
	}
	var disabledSkills []string
	if cfg.Options != nil {
		disabledSkills = cfg.Options.DisabledSkills
	}

	var cat nameCatalog
	for _, s := range skills.Filter(skills.Deduplicate(discovered), disabledSkills) {
		cat.skills = append(cat.skills, s.Name)
	}
	for name := range cfg.SetupAgentsWithDefaults(agentDefaults) {
		if name != config.AgentOrchestrator {
			cat.agents = append(cat.agents, name)
		}
	}
	return cat
}

// pluginAgentNames returns the names of the agent files in dir that parse.
func pluginAgentNames(dir string) map[string]struct{} {
	names := map[string]struct{}{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if _, err := prompt.ParseAgentMD(name, content); err != nil {
			continue
		}
		names[name] = struct{}{}
	}
	return names
}

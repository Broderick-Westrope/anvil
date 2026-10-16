package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
	"github.com/spf13/cobra"
)

var workflowCmd = newWorkflowCmd()

func newWorkflowCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "workflow",
		Short: "Work with workflow scripts",
	}
	root.AddCommand(newWorkflowCheckCmd())
	return root
}

func newWorkflowCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check <path>",
		Short: "Check a workflow script without running it",
		Long: `Parse a workflow script and check its requires() call without running any of its code.

<path> is a workflow script, or a command directory or COMMAND.md with a workflow: field.
Skills and agents are checked against what Anvil discovers with the current config.
Exits non-zero if the script doesn't parse, has a problem, or a requirement is missing.`,
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
				if cat, err = agent.DiscoverCatalog(store); err != nil {
					return err
				}
			}
			return checkWorkflow(cmd.OutOrStdout(), args[0], cat)
		},
	}
	cmd.Flags().Bool("no-catalog", false, "Skip loading config and checking skills and agents; check only parsing and references")
	return cmd
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
	for _, problem := range script.Problems {
		fmt.Fprintln(w, "error: "+problem)
	}

	if cat == nil {
		fmt.Fprintln(w, "note: skills and agents not checked (--no-catalog)")
		cat = allowAllCatalog{}
	}
	missing := script.Missing(cat)
	for _, m := range missing {
		fmt.Fprintln(w, "missing: "+m)
	}
	if len(missing) > 0 || len(script.Problems) > 0 {
		return errWorkflowCheckFailed
	}
	return nil
}

// resolveWorkflowPath returns path itself if it is a script, or the script
// a command directory or COMMAND.md points at. It returns an empty path and
// the reasons when there's no script to check.
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
	if len(cmd.Problems) > 0 {
		return "", cmd.Problems
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

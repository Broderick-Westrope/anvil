package prompt

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentMD holds the parsed content of an agent description file.
type AgentMD struct {
	// Name is the agent identifier (e.g. "explorer", "fixer").
	Name string

	// DelegatesTo lists agent names this agent is allowed to delegate to.
	DelegatesTo []string

	// Body is the markdown content after the frontmatter block.
	Body string
}

// agentFrontmatter is the YAML structure expected in agent .md files.
type agentFrontmatter struct {
	DelegatesTo []string `yaml:"delegates_to"`
}

// ParseAgentMD parses an agent description file with YAML frontmatter.
// The frontmatter must be delimited by "---" lines. The body is everything
// after the closing delimiter. If no frontmatter is present, the entire
// content is treated as the body.
func ParseAgentMD(name string, content []byte) (AgentMD, error) {
	agent := AgentMD{Name: name}

	// Look for frontmatter delimited by "---".
	const delim = "---"

	// Normalise line endings.
	text := string(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))

	if !strings.HasPrefix(strings.TrimLeft(text, "\n"), delim) {
		// No frontmatter — entire content is body.
		agent.Body = text
		return agent, nil
	}

	// Advance past the first "---".
	rest := strings.TrimLeft(text, "\n")
	rest = rest[len(delim):]

	// Find the closing "---".
	idx := strings.Index(rest, "\n"+delim)
	if idx == -1 {
		// Malformed frontmatter — treat whole content as body.
		agent.Body = text
		return agent, nil
	}

	yamlContent := rest[:idx]
	body := rest[idx+len("\n"+delim):]

	// Strip a single leading newline from the body if present.
	body = strings.TrimPrefix(body, "\n")

	var fm agentFrontmatter
	if err := yaml.Unmarshal([]byte(yamlContent), &fm); err != nil {
		return AgentMD{}, fmt.Errorf("parsing frontmatter for agent %q: %w", name, err)
	}

	agent.DelegatesTo = fm.DelegatesTo
	agent.Body = body
	return agent, nil
}

// BuildAgentsBlock generates the <Agents> block for the orchestrator prompt.
// Each agent is listed with its name and body content (role, capabilities,
// and routing guidance). The result is wrapped in <Agents> tags.
func BuildAgentsBlock(agents []AgentMD) string {
	var sb strings.Builder
	sb.WriteString("<Agents>\n")
	for _, a := range agents {
		sb.WriteString("\n@")
		sb.WriteString(a.Name)
		sb.WriteString("\n")
		body := strings.TrimSpace(a.Body)
		if body != "" {
			sb.WriteString(body)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n</Agents>")
	return sb.String()
}

// agentRoutingRules maps agent names to a short routing hint used in the
// delegation workflow. The hint is included only when the agent is enabled.
var agentRoutingRules = map[string]string{
	"oracle":          "Route deep reasoning, high-stakes architecture decisions, or persistent bugs to @oracle.",
	"explorer":        "Route broad codebase discovery and parallel search tasks to @explorer.",
	"librarian":       "Route external documentation lookup and unfamiliar library research to @librarian.",
	"designer":        "Route UI/UX work and user-facing polish to @designer.",
	"fixer":           "Route well-defined, bounded implementation work and test writing to @fixer.",
	"planner":         "Route feature planning, requirement interviews, and spec writing to @planner.",
	"tester":          "Route comprehensive test strategy, coverage analysis, and flaky-test diagnosis to @tester.",
	"reviewer":        "Route code review, diff analysis, and PR quality checks to @reviewer.",
	"devils-advocate": "Route adversarial review of specs and plans to @devils-advocate.",
}

// BuildDelegationWorkflow generates the <Workflow> block for the orchestrator
// prompt. It describes the 6-step routing pattern and includes dynamic
// validation routing rules based on the enabled agents.
func BuildDelegationWorkflow(agents []AgentMD) string {
	// Build the set of enabled agent names for dynamic rule inclusion.
	enabled := make(map[string]bool, len(agents))
	for _, a := range agents {
		enabled[a.Name] = true
	}

	// Collect per-agent routing rules for enabled agents.
	var rules strings.Builder
	for _, a := range agents {
		if rule, ok := agentRoutingRules[a.Name]; ok {
			rules.WriteString("  - ")
			rules.WriteString(rule)
			rules.WriteString("\n")
		}
	}

	// Build the comma-separated list of available agent names.
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, "@"+a.Name)
	}
	agentList := strings.Join(names, ", ")

	var sb strings.Builder
	sb.WriteString("<Workflow>\n")
	sb.WriteString("Follow these 6 steps on every turn:\n\n")

	sb.WriteString("1. **Understand** — Read the full request. Identify the goal, constraints, and\n")
	sb.WriteString("   any ambiguity. Ask clarifying questions before acting if the request is\n")
	sb.WriteString("   genuinely unclear.\n\n")

	sb.WriteString("2. **Path Selection** — Decide: handle directly or delegate?\n")
	sb.WriteString("   - Handle directly when the task is small, self-contained, or delegation\n")
	sb.WriteString("     overhead exceeds the work itself.\n")
	sb.WriteString("   - Delegate when a specialist brings meaningful value (deeper reasoning,\n")
	sb.WriteString("     faster search, bounded implementation, adversarial review).\n\n")

	sb.WriteString("3. **Delegation Check** — If delegating, verify the target agent is available.\n")
	if agentList != "" {
		sb.WriteString("   Available agents: ")
		sb.WriteString(agentList)
		sb.WriteString("\n")
	}
	sb.WriteString("   Routing rules:\n")
	if rules.Len() > 0 {
		sb.WriteString(rules.String())
	}
	sb.WriteString("   - Default: handle directly if no specialist is a clear fit.\n\n")

	sb.WriteString("4. **Split and Parallelize** — If multiple independent sub-tasks exist, fire\n")
	sb.WriteString("   them with multiple task calls in the same turn. All calls execute in\n")
	sb.WriteString("   parallel and results are returned together. Do not wait for one to finish\n")
	sb.WriteString("   before starting another.\n\n")

	sb.WriteString("5. **Execute** — Run delegated tasks or perform direct work. For delegated\n")
	sb.WriteString("   work: provide the specialist with complete context and a clear spec so it\n")
	sb.WriteString("   can execute without follow-up. Do not delegate vague or half-formed tasks.\n\n")

	sb.WriteString("6. **Verify** — Review results for correctness. Run diagnostics or tests if\n")
	sb.WriteString("   relevant. Synthesise sub-task results into a coherent response. If a result\n")
	sb.WriteString("   is wrong or incomplete, fix it directly or re-delegate with a corrected spec.\n")

	sb.WriteString("\n</Workflow>")
	return sb.String()
}

// ValidateDelegatesTo checks that all delegates_to references in the agents
// slice resolve to known agent names. It returns hard errors for references to
// agents that do not exist at all, and soft warnings for references to agents
// that exist but have been disabled.
func ValidateDelegatesTo(agents []AgentMD, disabledAgents []string) (errs []error, warnings []error) {
	// Build lookup sets.
	known := make(map[string]bool, len(agents))
	for _, a := range agents {
		known[a.Name] = true
	}

	disabled := make(map[string]bool, len(disabledAgents))
	for _, d := range disabledAgents {
		disabled[d] = true
	}

	for _, a := range agents {
		for _, ref := range a.DelegatesTo {
			if !known[ref] && !disabled[ref] {
				errs = append(errs, fmt.Errorf(
					"agent %q delegates_to unknown agent %q",
					a.Name, ref,
				))
			} else if disabled[ref] {
				warnings = append(warnings, fmt.Errorf(
					"agent %q delegates_to disabled agent %q",
					a.Name, ref,
				))
			}
		}
	}

	return errs, warnings
}

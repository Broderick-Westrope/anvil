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

	// Role is a one-line description used by the orchestrator for routing.
	Role string

	// DelegateWhen describes conditions under which the orchestrator should
	// delegate to this agent.
	DelegateWhen string

	// DontDelegateWhen describes conditions under which the orchestrator should
	// NOT delegate to this agent.
	DontDelegateWhen string

	// Model is the provider/model string (e.g. "anthropic/claude-opus-4-6").
	// Empty means inherit from the orchestrator.
	Model string

	// Tools is the allowed tools list. nil means all tools (unrestricted),
	// empty slice means no tools.
	Tools []string

	// Skills is the allowed skills list. nil means all skills (unrestricted),
	// empty slice means no skills.
	Skills []string

	// MCPs is the allowed MCP servers and tools. nil means all MCPs
	// (unrestricted), empty map means no MCPs. Map keys are server names;
	// nil values mean all tools from that server, non-nil values restrict
	// to specific tools.
	MCPs map[string][]string

	// RoutingHint is a short routing rule for the orchestrator's delegation
	// workflow (e.g. "Route deep reasoning to @oracle."). Empty means no
	// routing hint is emitted.
	RoutingHint string

	// Body is the full specialist system prompt (markdown body after the
	// frontmatter block).
	Body string
}

// agentFrontmatter is the YAML structure expected in agent .md files.
type agentFrontmatter struct {
	DelegatesTo      []string            `yaml:"delegates_to"`
	Role             string              `yaml:"role"`
	DelegateWhen     string              `yaml:"delegate_when"`
	DontDelegateWhen string              `yaml:"dont_delegate_when"`
	Model            string              `yaml:"model"`
	Tools            []string            `yaml:"tools"`
	Skills           []string            `yaml:"skills"`
	MCPs             map[string][]string `yaml:"mcps"`
	RoutingHint      string              `yaml:"routing_hint"`
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
	agent.Role = fm.Role
	agent.DelegateWhen = fm.DelegateWhen
	agent.DontDelegateWhen = fm.DontDelegateWhen
	agent.Model = fm.Model
	agent.Tools = fm.Tools
	agent.Skills = fm.Skills
	agent.MCPs = fm.MCPs
	agent.RoutingHint = fm.RoutingHint
	agent.Body = body
	return agent, nil
}

// BuildAgentsBlock generates the <Agents> block for the orchestrator prompt.
// Each agent is listed with its routing metadata (role, delegate_when,
// dont_delegate_when) sourced from frontmatter. The body is NOT included here;
// it is used only for the specialist's own system prompt. The result is wrapped
// in <Agents> tags.
func BuildAgentsBlock(agents []AgentMD) string {
	var sb strings.Builder
	sb.WriteString("<Agents>\n\n")
	for _, a := range agents {
		sb.WriteString("@" + a.Name + "\n")
		if a.Role != "" {
			sb.WriteString("- Role: " + a.Role + "\n")
		}
		if a.DelegateWhen != "" {
			sb.WriteString("- Delegate when: " + a.DelegateWhen + "\n")
		}
		if a.DontDelegateWhen != "" {
			sb.WriteString("- Don't delegate when: " + a.DontDelegateWhen + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("</Agents>")
	return sb.String()
}

// BuildDelegationWorkflow generates the <Workflow> block for the orchestrator
// prompt. It describes the 6-step routing pattern and includes dynamic
// validation routing rules based on the enabled agents.
func BuildDelegationWorkflow(agents []AgentMD) string {
	// Collect per-agent routing hints for enabled agents.
	var rules strings.Builder
	for _, a := range agents {
		if a.RoutingHint != "" {
			rules.WriteString("  - ")
			rules.WriteString(a.RoutingHint)
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

	// Detect cycles in the delegates_to graph.
	// Build adjacency list.
	adj := make(map[string][]string, len(agents))
	for _, a := range agents {
		adj[a.Name] = a.DelegatesTo
	}

	// DFS cycle detection.
	const (
		white = 0 // unvisited
		gray  = 1 // in current path
		black = 2 // fully visited
	)
	color := make(map[string]int, len(agents))

	var dfs func(node string) bool
	dfs = func(node string) bool {
		color[node] = gray
		for _, neighbor := range adj[node] {
			if color[neighbor] == gray {
				// Found a cycle — this is a hard error since cycles could
				// cause infinite delegation loops at runtime.
				errs = append(errs, fmt.Errorf(
					"delegation cycle detected: agent %q delegates to %q which is in the current delegation chain",
					node, neighbor,
				))
				return true
			}
			if _, inGraph := adj[neighbor]; inGraph && color[neighbor] == white {
				if dfs(neighbor) {
					return true
				}
			}
		}
		color[node] = black
		return false
	}

	for _, a := range agents {
		if color[a.Name] == white {
			dfs(a.Name)
		}
	}

	return errs, warnings
}

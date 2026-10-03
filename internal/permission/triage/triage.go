// Package triage finds repeated permission requests that narrow rules cover.
package triage

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/match"
	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
)

// Record is one logged permission decision used as triage evidence.
type Record struct {
	SessionID     string
	WorkingDir    string
	ToolName      string
	Input         string
	InputSegments []string
	DecidedBy     string
	Verdict       string
	MaxHazard     *float64
}

// Kind is the action a candidate rule would take.
type Kind string

// Candidate rule kinds.
const (
	KindAllow Kind = "allow"
	KindDeny  Kind = "deny"
)

// Tier ranks how much review a candidate needs before it is applied.
type Tier string

// Candidate tiers: A for curated safe families, B for uncurated patterns.
const (
	TierA Tier = "A"
	TierB Tier = "B"
)

// Candidate is a proposed permission rule with its supporting evidence.
type Candidate struct {
	Kind         Kind
	Tier         Tier
	ToolPattern  string
	InputPattern string
	Count        int
	Sessions     int
	Projects     int
	Examples     []string
	Warning      string
}

// Options tunes candidate thresholds and scope for Analyze.
type Options struct {
	MinCount       int
	MaxHazardAllow float64
	WorkingDir     string
}

type group struct {
	candidate Candidate
	sessions  map[string]bool
	projects  map[string]bool
	examples  []string
	unsafe    bool
}

// IsSource reports whether a decision can contribute candidate evidence.
func IsSource(r Record) bool {
	if r.Verdict == string(permission.VerdictCancelled) {
		return false
	}
	switch permission.DecisionSource(r.DecidedBy) {
	case permission.DecisionSourceHuman, permission.DecisionSourceBouncer,
		permission.DecisionSourceSessionGrant, permission.DecisionSourceSessionRule:
		return true
	default:
		return false
	}
}

// Analyze sorts candidates by tier, descending count, and pattern.
func Analyze(records []Record, rules []config.PermissionRule, opts Options) (allow, deny []Candidate) {
	allow, deny = []Candidate{}, []Candidate{}
	if opts.MinCount <= 0 {
		opts.MinCount = 5
	}
	if opts.MaxHazardAllow <= 0 {
		opts.MaxHazardAllow = 0.2
	}
	a := newAnalyzer(rules)
	ix := newEvidenceIndex(len(records))
	for _, r := range records {
		if opts.WorkingDir != "" && r.WorkingDir != opts.WorkingDir {
			continue
		}
		ix.add(r)
	}
	type allowResult struct {
		pattern string
		tier    Tier
	}
	allowPatterns := make(map[string]allowResult)
	groups := make(map[string]*group)
	for _, e := range ix.all {
		r := e.record
		if !IsSource(r) {
			continue
		}
		seen := make(map[string]bool)
		add := func(kind Kind, tier Tier, pattern, example, warning string) {
			key := string(kind) + "\x00" + r.ToolName + "\x00" + pattern
			g := groups[key]
			if g == nil {
				g = &group{candidate: Candidate{Kind: kind, Tier: tier, ToolPattern: r.ToolName, InputPattern: pattern, Warning: warning}, sessions: map[string]bool{}, projects: map[string]bool{}}
				groups[key] = g
			}
			if !seen[key] {
				g.candidate.Count++
				seen[key] = true
			}
			g.sessions[r.SessionID] = true
			g.projects[r.WorkingDir] = true
			if !slices.Contains(g.examples, example) {
				g.examples = append(g.examples, example)
			}
			if kind == KindAllow && (r.Verdict != string(permission.VerdictAllow) ||
				(r.MaxHazard != nil && (math.IsNaN(*r.MaxHazard) || *r.MaxHazard > opts.MaxHazardAllow))) {
				g.unsafe = true
			}
		}
		switch {
		case r.ToolName == "bash":
			for _, input := range e.inputs {
				res, ok := allowPatterns[input]
				if !ok {
					res.pattern, res.tier = allowPattern(input)
					allowPatterns[input] = res
				}
				if res.pattern != "" {
					warning := ""
					if res.tier == TierB {
						warning = "Uncurated command: review every argument this pattern permits"
					}
					add(KindAllow, res.tier, res.pattern, input, warning)
				}
			}
			// Only a human's denial proposes a permanent deny rule. The
			// bouncer also denies explicit actions the user simply didn't
			// mention (git push, gh pr merge), so learning denies from it
			// would turn its false positives into permanent rules. Its
			// denials still count as evidence against allow candidates.
			if len(e.commands) == 1 && r.Verdict == string(permission.VerdictDeny) &&
				r.DecidedBy == string(permission.DecisionSourceHuman) {
				if pattern := denyPattern(e.commands[0]); pattern != "" {
					add(KindDeny, TierB, pattern, e.commands[0], "Review the scope of this permanent denial")
				}
			}
		case strings.HasPrefix(r.ToolName, "mcp_") && !strings.ContainsAny(r.ToolName, globMeta):
			add(KindAllow, TierB, "", r.Input, "MCP rules cover every argument this tool accepts")
		}
	}
	for _, g := range groups {
		if g.unsafe || g.candidate.Count < opts.MinCount {
			continue
		}
		c := g.candidate
		valid, covered := a.validate(c, ix), true
		for _, example := range g.examples {
			action := a.action(c.ToolPattern, example)
			if action != config.PermissionAction(c.Kind) {
				covered = false
			}
			if c.Kind == KindAllow && action == config.PermissionDeny {
				valid = false
			}
		}
		if !valid || covered {
			continue
		}
		c.Sessions, c.Projects = len(g.sessions), len(g.projects)
		c.Examples = slices.Clone(g.examples[:min(3, len(g.examples))])
		if c.Kind == KindAllow {
			allow = append(allow, c)
		} else {
			deny = append(deny, c)
		}
	}
	order := func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(a.Tier, b.Tier), cmp.Compare(b.Count, a.Count), cmp.Compare(a.ToolPattern, b.ToolPattern), cmp.Compare(a.InputPattern, b.InputPattern))
	}
	slices.SortFunc(allow, order)
	slices.SortFunc(deny, order)
	return allow, deny
}

// globMeta lists characters that make a pattern token non-literal.
const globMeta = `*?[]{}\`

// analyzer caches compiled patterns and rule evaluations for one call;
// rules are fixed for its lifetime.
type analyzer struct {
	rules    []config.PermissionRule
	matchers map[string]*match.Matcher
	actions  map[[2]string]config.PermissionAction
}

func newAnalyzer(rules []config.PermissionRule) *analyzer {
	return &analyzer{rules: rules, matchers: map[string]*match.Matcher{}, actions: map[[2]string]config.PermissionAction{}}
}

func (a *analyzer) matches(pattern, input string) bool {
	m, ok := a.matchers[pattern]
	if !ok {
		m, _ = match.Compile(pattern)
		a.matchers[pattern] = m
	}
	return m != nil && m.Match(input)
}

func (a *analyzer) action(tool, input string) config.PermissionAction {
	key := [2]string{tool, input}
	action, ok := a.actions[key]
	if !ok {
		action = permission.Evaluate(tool, input, a.rules, nil).Action
		a.actions[key] = action
	}
	return action
}

// validate reports whether no matching evidence contradicts c, scanning
// only evidence that can match c's pattern.
func (a *analyzer) validate(c Candidate, ix *evidenceIndex) bool {
	for _, i := range ix.lookup(c) {
		e := &ix.all[i]
		if !a.matches(c.ToolPattern, e.record.ToolName) {
			continue
		}
		matched := c.InputPattern == ""
		if !matched {
			for _, input := range e.inputs {
				if a.matches(c.InputPattern, input) {
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		if (c.Kind == KindAllow && e.record.Verdict == string(permission.VerdictDeny)) ||
			(c.Kind == KindDeny && e.record.Verdict == string(permission.VerdictAllow)) {
			return false
		}
		if c.Kind != KindAllow {
			continue
		}
		for _, input := range e.inputs {
			if c.InputPattern != "" && !a.matches(c.InputPattern, input) {
				continue
			}
			if a.action(e.record.ToolName, input) == config.PermissionDeny {
				return false
			}
		}
	}
	return true
}

type evidence struct {
	record Record
	inputs []string
	// commands holds one normalised segment per command for denied bash
	// requests, used to tell a single command from a chain.
	commands []string
}

// evidenceIndex groups evidence by tool and by the first token of each
// input so candidate validation avoids a full scan.
type evidenceIndex struct {
	all     []evidence
	every   []int
	byTool  map[string][]int
	byToken map[string]map[string][]int
}

func newEvidenceIndex(n int) *evidenceIndex {
	return &evidenceIndex{all: make([]evidence, 0, n), every: make([]int, 0, n), byTool: map[string][]int{}, byToken: map[string]map[string][]int{}}
}

func (ix *evidenceIndex) add(r Record) {
	i := len(ix.all)
	inputs := recordInputs(r)
	commands := inputs
	if r.ToolName == "bash" && r.Input != "" && r.Verdict == string(permission.VerdictDeny) {
		commands = segment.Normalized(r.Input)
	}
	ix.all = append(ix.all, evidence{record: r, inputs: inputs, commands: commands})
	ix.every = append(ix.every, i)
	ix.byTool[r.ToolName] = append(ix.byTool[r.ToolName], i)
	tokens := ix.byToken[r.ToolName]
	if tokens == nil {
		tokens = map[string][]int{}
		ix.byToken[r.ToolName] = tokens
	}
	for _, input := range inputs {
		fields := strings.Fields(input)
		if len(fields) == 0 {
			continue
		}
		if refs := tokens[fields[0]]; len(refs) == 0 || refs[len(refs)-1] != i {
			tokens[fields[0]] = append(refs, i)
		}
	}
}

// lookup returns the indices of evidence that could match c. It falls
// back to broader scans whenever the pattern is not a literal prefix.
func (ix *evidenceIndex) lookup(c Candidate) []int {
	if strings.ContainsAny(c.ToolPattern, globMeta) {
		return ix.every
	}
	if c.InputPattern == "" {
		return ix.byTool[c.ToolPattern]
	}
	token, ok := literalFirstToken(c.InputPattern)
	if !ok {
		return ix.byTool[c.ToolPattern]
	}
	return ix.byToken[c.ToolPattern][token]
}

// literalFirstToken returns the first token of pattern when every input
// matching pattern must share it as its first whitespace-separated field.
func literalFirstToken(pattern string) (string, bool) {
	if pattern == "" || unicode.IsSpace([]rune(pattern)[0]) {
		return "", false
	}
	token := strings.Fields(pattern)[0]
	if strings.ContainsAny(token, globMeta) {
		return "", false
	}
	return token, true
}

var verbRE = regexp.MustCompile(`^[a-z][a-z-]*$`)

func denyPattern(input string) string {
	if !simpleSegment(input) {
		return ""
	}
	tokens := strings.Fields(input)
	n := min(2, len(tokens))
	if len(tokens) >= 3 && verbRE.MatchString(tokens[1]) && verbRE.MatchString(tokens[2]) {
		n = 3
	}
	if len(tokens) < 2 || !verbRE.MatchString(tokens[0]) || !verbRE.MatchString(tokens[1]) {
		return ""
	}
	return strings.Join(tokens[:n], " ") + " *"
}

// recordInputs returns the segments a record is evaluated as. Bash
// commands are re-split rather than trusting logged segments, so evidence
// recorded by an older segmenter is judged by today's rules.
func recordInputs(r Record) []string {
	if r.ToolName != "bash" {
		return []string{r.Input}
	}
	if r.Input == "" && len(r.InputSegments) > 0 {
		return r.InputSegments
	}
	return segment.Split(r.Input)
}

// Conflict describes evidence that contradicts a proposed rule set.
type Conflict struct {
	ToolName string
	Input    string
	Reason   string
}

// Simulate uses the same upsert semantics as SetPermissionRule, without mutation.
func Simulate(rules []config.PermissionRule, chosen []Candidate, evidence []Record) []Conflict {
	rules = slices.Clone(rules)
	for i := range rules {
		rules[i].SubRules = slices.Clone(rules[i].SubRules)
	}
	for _, c := range chosen {
		rules = config.UpsertPermissionRule(rules, c.ToolPattern, c.InputPattern, config.PermissionAction(c.Kind))
	}
	return Check(rules, chosen, evidence)
}

// Check validates effective rules against denials and every selected example.
func Check(rules []config.PermissionRule, chosen []Candidate, evidence []Record) []Conflict {
	var conflicts []Conflict
	a := newAnalyzer(rules)
	for _, r := range evidence {
		if r.DecidedBy == string(permission.DecisionSourceHuman) && r.Verdict == string(permission.VerdictDeny) &&
			permission.EvaluateAll(r.ToolName, recordInputs(r), rules, nil).Action == config.PermissionAllow {
			conflicts = append(conflicts, Conflict{r.ToolName, r.Input, "Human-denied request would be allowed"})
		}
	}
	for _, c := range chosen {
		for _, example := range c.Examples {
			if a.action(c.ToolPattern, example) != config.PermissionAction(c.Kind) {
				conflicts = append(conflicts, Conflict{c.ToolPattern, example, "Example does not evaluate to " + string(c.Kind)})
			}
		}
	}
	return conflicts
}

// Package triage finds repeated permission requests that narrow rules cover.
package triage

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
)

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

type Kind string

const (
	KindAllow Kind = "allow"
	KindDeny  Kind = "deny"
)

type Tier string

const (
	TierA Tier = "A"
	TierB Tier = "B"
)

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
	case permission.DecisionSourceHuman, permission.DecisionSourceAssessor,
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
	evidence := make([]Record, 0, len(records))
	groups := make(map[string]*group)
	for _, r := range records {
		if opts.WorkingDir != "" && r.WorkingDir != opts.WorkingDir {
			continue
		}
		evidence = append(evidence, r)
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
			inputs := recordInputs(r)
			for _, input := range inputs {
				pattern, tier := allowPattern(input)
				if pattern != "" {
					warning := ""
					if tier == TierB {
						warning = "Uncurated command: review every argument this pattern permits"
					}
					add(KindAllow, tier, pattern, input, warning)
				}
			}
			if len(inputs) == 1 && r.Verdict == string(permission.VerdictDeny) &&
				(r.DecidedBy == string(permission.DecisionSourceHuman) || r.DecidedBy == string(permission.DecisionSourceAssessor)) {
				if pattern := denyPattern(inputs[0]); pattern != "" {
					add(KindDeny, TierB, pattern, inputs[0], "Review the scope of this permanent denial")
				}
			}
		case strings.HasPrefix(r.ToolName, "mcp_") && !strings.ContainsAny(r.ToolName, "*?[]{}\\"):
			add(KindAllow, TierB, "", r.Input, "MCP rules cover every argument this tool accepts")
		}
	}
	for _, g := range groups {
		if g.unsafe || g.candidate.Count < opts.MinCount {
			continue
		}
		c := g.candidate
		valid, covered := true, true
		for _, r := range evidence {
			if !recordMatches(c, r) {
				continue
			}
			if (c.Kind == KindAllow && r.Verdict == string(permission.VerdictDeny)) ||
				(c.Kind == KindDeny && r.Verdict == string(permission.VerdictAllow)) {
				valid = false
				break
			}
			for _, input := range recordInputs(r) {
				if c.InputPattern != "" && !matches(c.InputPattern, input) {
					continue
				}
				if c.Kind == KindAllow && permission.Evaluate(r.ToolName, input, rules, nil).Action == config.PermissionDeny {
					valid = false
				}
			}
		}
		for _, example := range g.examples {
			action := permission.Evaluate(c.ToolPattern, example, rules, nil).Action
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

func recordInputs(r Record) []string {
	if r.ToolName != "bash" {
		return []string{r.Input}
	}
	if len(r.InputSegments) > 0 {
		return r.InputSegments
	}
	return segment.Split(r.Input)
}

func recordMatches(c Candidate, r Record) bool {
	if !matches(c.ToolPattern, r.ToolName) {
		return false
	}
	if c.InputPattern == "" {
		return true
	}
	for _, input := range recordInputs(r) {
		if matches(c.InputPattern, input) {
			return true
		}
	}
	return false
}

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
	for _, r := range evidence {
		if r.DecidedBy == string(permission.DecisionSourceHuman) && r.Verdict == string(permission.VerdictDeny) &&
			permission.EvaluateAll(r.ToolName, recordInputs(r), rules, nil).Action == config.PermissionAllow {
			conflicts = append(conflicts, Conflict{r.ToolName, r.Input, "Human-denied request would be allowed"})
		}
	}
	for _, c := range chosen {
		for _, example := range c.Examples {
			if permission.Evaluate(c.ToolPattern, example, rules, nil).Action != config.PermissionAction(c.Kind) {
				conflicts = append(conflicts, Conflict{c.ToolPattern, example, "Example does not evaluate to " + string(c.Kind)})
			}
		}
	}
	return conflicts
}

package permission

import (
	"log/slog"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission/match"
)

// EvaluateResult holds the outcome of rule evaluation.
type EvaluateResult struct {
	Action      config.PermissionAction
	MatchedRule string // The pattern that produced this action (for logging).
	IsDefault   bool   // True if no rule matched, using default "ask".
}

// Evaluate walks the ordered rule list and returns the final action.
// Rules are evaluated in order; last match wins.
// configRules are from merged user+project config.
// sessionRules are ephemeral grants for the current session.
// Session grants can upgrade ask→allow but NOT deny→allow (enforced here).
func Evaluate(
	toolName string,
	toolInput string,
	configRules []config.PermissionRule,
	sessionRules []config.PermissionRule,
) EvaluateResult {
	configResult := evaluateRules(toolName, toolInput, configRules)

	// Walk session rules for a possible upgrade.
	sessionResult := evaluateRules(toolName, toolInput, sessionRules)

	// If no config rule matched and no session rule matched, default to ask.
	if !configResult.matched && !sessionResult.matched {
		return EvaluateResult{
			Action:    config.PermissionAsk,
			IsDefault: true,
		}
	}

	// If no session rule matched, use the config result.
	if !sessionResult.matched {
		return EvaluateResult{
			Action:      configResult.action,
			MatchedRule: configResult.pattern,
		}
	}

	// If no config rule matched, use the session result.
	if !configResult.matched {
		return EvaluateResult{
			Action:      sessionResult.action,
			MatchedRule: sessionResult.pattern,
		}
	}

	// Both matched. Session rules cannot override deny.
	if configResult.action == config.PermissionDeny {
		slog.Debug("Session rule cannot override config deny",
			"tool", toolName,
			"config_pattern", configResult.pattern,
			"session_pattern", sessionResult.pattern,
		)
		return EvaluateResult{
			Action:      config.PermissionDeny,
			MatchedRule: configResult.pattern,
		}
	}

	// Session rule takes effect.
	return EvaluateResult{
		Action:      sessionResult.action,
		MatchedRule: sessionResult.pattern,
	}
}

// EvaluateAll evaluates multiple inputs against the rules and combines
// the results, worst outcome first: any deny wins, then any ask (or
// unmatched input), and only if every input is allowed is the overall
// action allow. The returned MatchedRule references the deciding
// input's rule.
//
// An empty inputs slice is evaluated as a single empty input
// (tool-name-only matching).
func EvaluateAll(
	toolName string,
	inputs []string,
	configRules []config.PermissionRule,
	sessionRules []config.PermissionRule,
) EvaluateResult {
	if len(inputs) == 0 {
		return Evaluate(toolName, "", configRules, sessionRules)
	}

	var (
		denyResult *EvaluateResult
		askResult  *EvaluateResult
		lastResult EvaluateResult
	)

	for _, input := range inputs {
		result := Evaluate(toolName, input, configRules, sessionRules)
		slog.Debug("Permission segment evaluated",
			"tool", toolName,
			"input", input,
			"action", result.Action,
			"matched_rule", result.MatchedRule,
		)
		switch result.Action {
		case config.PermissionDeny:
			if denyResult == nil {
				denyResult = &result
			}
		case config.PermissionAsk:
			if askResult == nil {
				askResult = &result
			}
		}
		lastResult = result
	}

	if denyResult != nil {
		return *denyResult
	}
	if askResult != nil {
		return *askResult
	}
	return lastResult
}

// ruleResult holds the intermediate result of evaluating a rule list.
type ruleResult struct {
	matched bool
	action  config.PermissionAction
	pattern string
}

// evaluateRules walks rules in order, returning the last matching result.
//
// Note: when a rule's tool pattern matches but none of its sub-rules
// match the input, the rule is treated as not matched — any earlier
// match (e.g. a broad "*": "allow") stands. To make a tool's sub-rules
// exhaustive, include a catch-all sub-rule such as "*": "ask".
func evaluateRules(toolName, toolInput string, rules []config.PermissionRule) ruleResult {
	var result ruleResult

	for _, rule := range rules {
		toolMatched, err := match.Match(rule.ToolPattern, toolName)
		if err != nil {
			slog.Debug("Pattern match error", "pattern", rule.ToolPattern, "input", toolName, "error", err)
			continue
		}
		if !toolMatched {
			continue
		}

		if len(rule.SubRules) > 0 {
			// Walk sub-rules; last matching sub-rule wins.
			for _, sub := range rule.SubRules {
				inputMatched, err := match.Match(sub.InputPattern, toolInput)
				if err != nil {
					slog.Debug("Pattern match error", "pattern", sub.InputPattern, "input", toolInput, "error", err)
					continue
				}
				if !inputMatched {
					continue
				}
				result = ruleResult{
					matched: true,
					action:  sub.Action,
					pattern: rule.ToolPattern + ":" + sub.InputPattern,
				}
				slog.Debug("Permission sub-rule matched",
					"tool", toolName,
					"input", toolInput,
					"tool_pattern", rule.ToolPattern,
					"input_pattern", sub.InputPattern,
					"action", sub.Action,
				)
			}
		} else {
			// Tool-level action.
			result = ruleResult{
				matched: true,
				action:  rule.Action,
				pattern: rule.ToolPattern,
			}
			slog.Debug("Permission rule matched",
				"tool", toolName,
				"pattern", rule.ToolPattern,
				"action", rule.Action,
			)
		}
	}

	return result
}

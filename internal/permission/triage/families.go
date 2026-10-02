package triage

import (
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/permission/match"
	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
)

var safeFamilies = map[string]string{
	"git status *":    "Reports workspace/index status; arguments cannot select another command or output file",
	"git rev-parse *": "Reports revisions, paths, or parsed arguments without executing them or writing files",
}

var needsSubcommand = map[string]bool{
	"git": true, "gh": true, "go": true, "npm": true, "pnpm": true,
	"yarn": true, "cargo": true, "docker": true, "kubectl": true,
	"gcloud": true, "aws": true, "terraform": true, "task": true,
	"uv": true, "pip": true, "brew": true, "bq": true,
}

func simpleSegment(input string) bool {
	return strings.TrimSpace(input) != "" && !segment.IsRedirect(input) &&
		!strings.ContainsAny(input, "`$|&;<>(){}")
}

func allowPattern(input string) (string, Tier) {
	if !simpleSegment(input) {
		return "", TierB
	}
	for pattern := range safeFamilies {
		if matches(pattern, input) {
			return pattern, TierA
		}
	}
	pattern := segment.Generalize(input)
	if strings.HasPrefix(input, "gh ") {
		pattern = denyPattern(input)
		if len(strings.Fields(pattern)) != 4 {
			return "", TierB
		}
	}
	prefix := strings.TrimSuffix(pattern, " *")
	tokens := strings.Fields(prefix)
	if len(tokens) == 0 || strings.ContainsAny(prefix, "*?[]\\") ||
		(len(tokens) == 1 && needsSubcommand[tokens[0]]) {
		return "", TierB
	}
	return pattern, TierB
}

func matches(pattern, input string) bool {
	ok, err := match.Match(pattern, input)
	return err == nil && ok
}

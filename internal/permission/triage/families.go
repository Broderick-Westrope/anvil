package triage

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/permission/match"
	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
)

var safeFamilies = map[string]string{
	"git status *":    "Reports workspace/index status; arguments cannot select another command or output file",
	"git rev-parse *": "Reports revisions, paths, or parsed arguments without executing them or writing files",
}

type compiledFamily struct {
	pattern string
	matcher *match.Matcher
}

var compiledFamilies = func() []compiledFamily {
	families := make([]compiledFamily, 0, len(safeFamilies))
	for _, pattern := range slices.Sorted(maps.Keys(safeFamilies)) {
		m, err := match.Compile(pattern)
		if err != nil {
			panic(err)
		}
		families = append(families, compiledFamily{pattern, m})
	}
	return families
}()

var needsSubcommand = map[string]bool{
	"git": true, "gh": true, "go": true, "npm": true, "pnpm": true,
	"yarn": true, "cargo": true, "docker": true, "kubectl": true,
	"gcloud": true, "aws": true, "terraform": true, "task": true,
	"uv": true, "pip": true, "brew": true, "bq": true,
}

// neverPropose lists commands whose arguments can destroy data, execute
// arbitrary code, escalate privileges, or reach remote systems. No allow
// candidate is ever produced for them; deny candidates are unaffected.
var neverPropose = map[string]bool{
	// Destructive file operations.
	"rm": true, "rmdir": true, "dd": true, "mv": true, "cp": true,
	"chmod": true, "chown": true, "chgrp": true, "ln": true,
	"truncate": true, "shred": true, "tee": true, "mkfs": true,
	"mount": true, "umount": true,
	// Privilege escalation and command wrappers.
	"sudo": true, "doas": true, "su": true, "env": true, "xargs": true,
	"eval": true, "exec": true, "source": true, ".": true,
	// Shells and interpreters.
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true,
	"python": true, "python3": true, "node": true, "deno": true,
	"bun": true, "ruby": true, "perl": true, "php": true, "lua": true,
	"osascript": true, "awk": true, "gawk": true, "base64": true,
	"find": true, "make": true, "open": true,
	// Package runners.
	"npx": true, "bunx": true, "pnpx": true,
	// Network access.
	"curl": true, "wget": true, "ssh": true, "scp": true, "sftp": true,
	"rsync": true, "nc": true, "ncat": true, "socat": true, "telnet": true,
	// Process and service control.
	"kill": true, "pkill": true, "killall": true, "crontab": true,
	"launchctl": true, "systemctl": true,
	// Cloud CLIs.
	"gcloud": true, "aws": true,

	"git push": true, "git reset": true, "git clean": true,
	"git checkout": true, "git restore": true, "git rebase": true,
	"git stash": true, "git filter-branch": true, "git filter-repo": true,
	"git config": true, "git remote": true, "git update-ref": true,
	"git reflog": true, "git gc": true, "git worktree": true,
	"npm run": true, "npm exec": true, "npm install": true, "npm i": true,
	"npm ci": true, "pnpm run": true, "pnpm exec": true, "pnpm dlx": true,
	"pnpm install": true, "pnpm add": true, "yarn run": true,
	"yarn dlx": true, "yarn add": true, "uv run": true, "uv pip": true,
	"pip install": true, "pip3 install": true, "go run": true,
	"go generate": true, "go install": true, "go get": true,
	"cargo run": true, "cargo install": true, "docker run": true,
	"docker exec": true, "docker rm": true, "docker rmi": true,
	"docker system": true, "kubectl exec": true, "kubectl delete": true,
	"kubectl apply": true, "kubectl patch": true, "kubectl edit": true,
	"kubectl scale": true, "kubectl rollout": true,
	"terraform apply": true, "terraform destroy": true,
	"terraform import": true, "gh api": true, "gh repo": true,
	"gh release": true, "gh secret": true, "gh auth": true,
	"brew install": true, "brew uninstall": true,
}

// neverProposeHeads holds the first token of every two-token entry, so a
// pattern that keeps only that token (e.g. "pip3 *" from
// "pip3 -q install x") cannot cover a blocked subcommand.
var neverProposeHeads = func() map[string]bool {
	heads := map[string]bool{}
	for prefix := range neverPropose {
		if head, _, ok := strings.Cut(prefix, " "); ok {
			heads[head] = true
		}
	}
	return heads
}()

func blocked(input string) bool {
	tokens := strings.Fields(input)
	if len(tokens) == 0 {
		return false
	}
	head := filepath.Base(tokens[0])
	if neverPropose[head] {
		return true
	}
	return len(tokens) > 1 && neverPropose[head+" "+tokens[1]]
}

func simpleSegment(input string) bool {
	return strings.TrimSpace(input) != "" && !segment.IsRedirect(input) &&
		!strings.ContainsAny(input, "`$|&;<>(){}")
}

func allowPattern(input string) (string, Tier) {
	if !simpleSegment(input) || blocked(input) {
		return "", TierB
	}
	for _, f := range compiledFamilies {
		if f.matcher.Match(input) {
			return f.pattern, TierA
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
		(len(tokens) == 1 && (needsSubcommand[tokens[0]] || neverProposeHeads[filepath.Base(tokens[0])])) {
		return "", TierB
	}
	return pattern, TierB
}

func matches(pattern, input string) bool {
	ok, err := match.Match(pattern, input)
	return err == nil && ok
}

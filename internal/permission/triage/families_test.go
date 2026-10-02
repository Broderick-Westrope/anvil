package triage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafeFamilies(t *testing.T) {
	t.Parallel()
	tests := map[string][]string{
		"git status *":    {"git push", "git reset --hard", "git -C /tmp status", "git -c core.fsmonitor=evil status", "git diff --output=/etc/hosts"},
		"git rev-parse *": {"git push", "git update-ref HEAD deadbeef", "git -c alias.rev-parse=evil rev-parse", "git rev-list --objects --all"},
	}
	require.Len(t, safeFamilies, len(tests))
	for pattern, dangerous := range tests {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			require.NotEmpty(t, safeFamilies[pattern])
			for _, input := range dangerous {
				require.False(t, matches(pattern, input), input)
			}
		})
	}
}

func TestDroppedFamilies(t *testing.T) {
	t.Parallel()
	dropped := map[string]string{
		"git diff *":      "--output writes arbitrary files; --ext-diff executes helpers",
		"git log *":       "Diff options include --output and --ext-diff",
		"git show *":      "Diff options include --output and --ext-diff",
		"git blame *":     "--contents reads arbitrary files and --textconv executes helpers",
		"git ls-files *":  "--exclude-from reads arbitrary files as patterns",
		"go build *":      "-o writes arbitrary files and -toolexec executes arbitrary programs",
		"go test *":       "Only -exec is blocked; -toolexec and -o remain unrestricted",
		"go vet *":        "-vettool executes an arbitrary binary",
		"go list *":       "-export builds packages and accepts -toolexec",
		"go mod tidy *":   "-modfile selects writable module files outside the workspace",
		"gofumpt *":       "-w rewrites arbitrary paths outside the workspace",
		"gh pr view *":    "--repo or a URL can contact arbitrary hosts; --web launches a browser",
		"gh pr list *":    "--repo can contact arbitrary hosts; --web launches a browser",
		"gh pr diff *":    "--repo or a URL can contact arbitrary hosts; --web launches a browser",
		"gh pr checks *":  "--repo can contact arbitrary hosts and --watch can run indefinitely",
		"gh issue view *": "--repo or a URL can contact arbitrary hosts; --web launches a browser",
		"gh issue list *": "--repo can contact arbitrary hosts; --web launches a browser",
		"gh run view *":   "--repo can contact arbitrary hosts; --web launches a browser",
		"gh run list *":   "--repo can contact arbitrary hosts beyond the project",
		"task test *":     "Additional task names and --taskfile can execute unrelated arbitrary code",
		"task lint *":     "Additional task names and --taskfile can execute unrelated arbitrary code",
		"task fmt *":      "Additional task names and --taskfile can execute unrelated arbitrary code",
		"wc *":            "Reads arbitrary files, including secrets outside the workspace",
		"head *":          "Reads arbitrary files, including secrets outside the workspace",
		"tail *":          "Reads arbitrary secrets; -f also runs indefinitely",
		"find *":          "-delete removes files and -exec executes arbitrary programs",
		"sed *":           "-i modifies arbitrary files",
		"rg *":            "--pre executes arbitrary programs",
		"awk *":           "system executes arbitrary programs",
		"xargs *":         "Executes arbitrary programs",
		"cat *":           "Reads arbitrary secrets outside the workspace",
		"less *":          "Reads arbitrary secrets and permits shell escapes",
		"python*":         "Executes arbitrary code",
		"python *":        "Executes arbitrary code",
		"python3 *":       "Executes arbitrary code",
		"node *":          "Executes arbitrary code",
		"bash *":          "Executes arbitrary code",
		"sh *":            "Executes arbitrary code",
		"npm run *":       "Executes arbitrary project scripts",
		"npm exec *":      "Downloads and executes arbitrary programs",
		"pnpm dlx *":      "Downloads and executes arbitrary programs",
		"make *":          "Executes arbitrary targets or alternate makefiles",
		"npm *":           "Can install or publish packages",
		"docker *":        "Can mount host files and run arbitrary containers",
		"kubectl *":       "Can mutate remote clusters",
		"gcloud *":        "Can mutate remote cloud resources",
		"aws *":           "Can mutate remote cloud resources",
		"terraform *":     "Can mutate remote infrastructure",
	}
	for pattern, reason := range dropped {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()
			require.NotContains(t, safeFamilies, pattern, reason)
		})
	}
}

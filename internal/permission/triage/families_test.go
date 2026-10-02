package triage

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
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

func TestNeverPropose(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"rm -rf build", "/bin/rm -rf x", "/usr/bin/rm x", "rmdir out", "dd if=/dev/zero of=x",
		"mv a b", "cp a b", "chmod 777 x", "chown me x", "chgrp g x", "ln -s a b", "truncate -s 0 x",
		"shred x", "sudo ls", "doas ls", "su root", "env FOO=1 ls", "xargs rm", "eval foo",
		"exec ls", "source env.sh", ". env.sh", "sh script.sh", "bash script.sh", "zsh -c x",
		"fish x", "dash x", "python x.py", "python3 x.py", "node x.js", "deno run x", "bun x",
		"ruby x", "perl x", "php x", "lua x", "osascript x", "npx jest", "bunx jest", "pnpx jest",
		"curl https://x", "wget https://x", "ssh host", "scp a host:b", "sftp host", "rsync a b",
		"nc host 1", "ncat host 1", "socat a b", "telnet host", "kill 1", "pkill x", "killall x",
		"crontab -e", "launchctl load x", "systemctl stop x", "open x", "make all", "tee out.txt",
		"mkfs x", "mount x", "umount x", "base64 -d x", "awk x", "gawk x", "find . -name x",
		"gcloud compute list", "aws s3 ls", "/opt/homebrew/bin/aws s3 ls",
		"git push origin main", "git reset --hard", "git clean -fd", "git checkout main",
		"git restore x", "git rebase main", "git stash pop", "git filter-branch x",
		"git filter-repo x", "git config x y", "git remote add x", "git update-ref x",
		"git reflog expire", "git gc", "git worktree add x", "/usr/bin/git push",
		"npm run build", "npm exec jest", "npm install x", "npm i x", "npm ci", "pnpm run build",
		"pnpm exec jest", "pnpm dlx x", "pnpm install", "pnpm add x", "yarn run x", "yarn dlx x",
		"yarn add x", "uv run x", "uv pip install x", "pip install x", "pip3 install x",
		"pip3 -q install x", "go run .", "go generate ./...", "go install x", "go get x",
		"cargo run", "cargo install x", "docker run x", "docker exec x", "docker rm x",
		"docker rmi x", "docker system prune", "kubectl exec x", "kubectl delete x",
		"kubectl apply -f x", "kubectl patch x", "kubectl edit x", "kubectl scale x",
		"kubectl rollout restart x", "terraform apply", "terraform destroy", "terraform import x",
		"gh api repos/x", "gh repo delete x", "gh release create x", "gh secret set x",
		"gh auth token", "brew install x", "brew uninstall x",
	}
	for _, input := range inputs {
		pattern, _ := allowPattern(input)
		require.Empty(t, pattern, input)
	}
	for input, want := range map[string]string{
		"gh pr view 1":       "gh pr view *",
		"sed -n 1p x":        "sed *",
		"git status --short": "git status *",
		"go test ./...":      "go test *",
		"ls -la":             "ls *",
	} {
		pattern, _ := allowPattern(input)
		require.Equal(t, want, pattern, input)
	}
	a, d := Analyze(repeated("git push origin main", "deny"), nil, Options{})
	require.Empty(t, a)
	require.Len(t, d, 1)
	require.Equal(t, "git push origin *", d[0].InputPattern)
}

func TestQuotedHeadsNeverProposed(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"'rm' -rf /tmp/x", `"curl" https://evil`, "'sudo' ls", `\rm -rf x`, "r''m x",
		"./rm x", `git "push" origin`, "'git' push", `gh "pr" merge 1`, "gh pr 'merge' 1",
		"--opt=x -la", "l*s -la",
	} {
		pattern, _ := allowPattern(input)
		require.Empty(t, pattern, input)
		a, _ := Analyze(repeated(input, "allow"), nil, Options{})
		require.Empty(t, a, input)
	}
	for _, input := range []string{"'rm' -rf /tmp/x", `"curl" https://evil`, "'sudo' ls", `m -rf x`, "r''m x"} {
		_, d := Analyze(repeated(input, "deny"), nil, Options{})
		require.Empty(t, d, input)
	}
}

func TestWrappersNeverProposed(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"nohup rm -rf /x", "watch rm -rf x", "command rm -rf x", "timeout 30 rm -rf x",
		"nice rm -rf x", "setsid rm -rf x", "stdbuf -oL rm -rf x", "ionice -c3 rm -rf x",
		"/usr/bin/nohup rm -rf x",
	} {
		pattern, _ := allowPattern(input)
		require.Empty(t, pattern, input)
		a, _ := Analyze(repeated(input, "allow"), nil, Options{})
		require.Empty(t, a, input)
	}
	for _, wrapper := range []string{"command", "doas", "env", "exec", "ionice", "nice", "nohup", "setsid", "stdbuf", "sudo", "time", "timeout", "watch", "xargs"} {
		require.True(t, segment.IsWrapper(wrapper), wrapper)
		for _, input := range []string{wrapper + " ls -la", wrapper + " status", wrapper} {
			pattern, _ := allowPattern(input)
			require.Empty(t, pattern, input)
		}
	}
	require.False(t, segment.IsWrapper("ls"))
	a, _ := Analyze(repeated("nohup ls -la", "allow"), nil, Options{})
	require.Len(t, a, 1)
	require.Equal(t, "ls *", a[0].InputPattern)
	require.Equal(t, []string{"ls -la"}, a[0].Examples)
}

func TestRemoteGhVerbsNeverProposed(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"gh pr merge 42", "gh workflow run deploy", "gh issue create -t x", "gh pr create",
		"gh pr close 1", "gh pr edit 1", "gh pr comment 1", "gh pr review 1", "gh pr ready 1",
		"gh issue close 1", "gh issue edit 1", "gh issue comment 1", "gh issue delete 1",
		"gh workflow enable x", "gh workflow disable x", "gh run rerun 1", "gh run cancel 1",
		"gh gist create x", "gh gist edit x", "gh gist delete x", "gh label create x",
		"gh variable set x", "gh ssh-key add x", "gh gpg-key add x", "/usr/bin/gh pr merge 1",
	} {
		pattern, _ := allowPattern(input)
		require.Empty(t, pattern, input)
		a, _ := Analyze(repeated(input, "allow"), nil, Options{})
		require.Empty(t, a, input)
	}
	for input, want := range map[string]string{
		"gh pr view 42":  "gh pr view *",
		"gh pr list":     "gh pr list *",
		"gh pr diff 1":   "gh pr diff *",
		"gh pr checks 1": "gh pr checks *",
	} {
		a, _ := Analyze(repeated(input, "allow"), nil, Options{})
		require.Len(t, a, 1, input)
		require.Equal(t, want, a[0].InputPattern, input)
		require.Equal(t, TierB, a[0].Tier, input)
	}
}

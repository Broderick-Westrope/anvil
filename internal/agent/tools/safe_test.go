package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContainsUnsafeShellSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"plain ls", "ls -la", false},
		{"plain echo", "echo hello world", false},
		{"plain git status", "git status", false},
		{"dollar sign in argument", "echo $HOME", false},
		{"empty string", "", false},
		{"pipe", "ls | grep foo", true},
		{"double ampersand", "ls && echo done", true},
		{"double pipe", "ls || echo fail", true},
		{"semicolon", "ls; echo done", true},
		{"backticks", "ls `echo foo`", true},
		{"command substitution", "ls $(echo foo)", true},
		{"background ampersand", "ls & echo done", true},
		{"output redirect", "ls > /tmp/out", true},
		{"append redirect", "echo x >> /tmp/out", true},
		{"input redirect", "cat < /etc/passwd", true},
		{"ampersand gt redirect", "ls &> /dev/null", true},
		{"gt ampersand redirect", "ls >& /dev/null", true},
		{"newline", "ls\ntouch x", true},
		{"carriage return", "ls\rtouch x", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := containsUnsafeShellSyntax(tt.input)
			assert.Equal(t, tt.expected, got, "containsUnsafeShellSyntax(%q)", tt.input)
		})
	}
}

func TestIsSafeReadOnly(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"ls",
		"ls -la",
		"LS -la",
		"ls *.go",
		"pwd",
		"echo hello world",
		"echo 'quoted words'",
		"du -sh .",
		"ps aux",
		"printenv PATH",
		"which go",
		"uname -a",
		"date",
		"date +%Y-%m-%d",
		"date -u",
		"date -u +%s",
		"date -Iseconds",
		"hostname",
		"hostname -s",
		"git status",
		"git status --short",
		"git log --oneline -5",
		"git log --format='%h %s'",
		"git diff",
		"git diff --stat HEAD~1",
		"git show HEAD",
		"git blame -L 1,10 main.go",
		"git grep -n foo",
		"git rev-parse --show-toplevel",
		"git ls-files",
		"git describe --tags",
		"git config --get user.name",
		"git config --list",
		"git branch",
		"git branch -a",
		"git branch -vv",
		"git branch --show-current",
		"git branch --contains HEAD",
		"git branch --merged main",
		"git tag",
		"git tag -l",
		"git tag -n",
		"git tag --points-at HEAD",
		"git tag --contains v1.0.0",
		"git remote",
		"git remote -v",
		"git remote get-url origin",
		"git remote show -n origin",
	}
	for _, command := range allowed {
		t.Run("allow/"+command, func(t *testing.T) {
			t.Parallel()
			assert.True(t, isSafeReadOnly(command), "isSafeReadOnly(%q)", command)
		})
	}

	denied := []string{
		// Not on the list.
		"rm -rf /tmp/x",
		"touch x",
		"/bin/ls",
		"lsof",
		// Wrappers that run another program.
		"env",
		"env sh -c 'touch x'",
		"env LD_PRELOAD=/tmp/x.so ls",
		"env GIT_EXTERNAL_DIFF=/tmp/x git diff",
		"nohup touch x",
		"nice touch x",
		"time touch x",
		"timeout 5 touch x",
		"kill 1234",
		"killall node",
		// Shell syntax.
		"echo pwned > x",
		"ls & touch x",
		"ls\ntouch x",
		"FOO=bar ls",
		"ls $(touch x)",
		"echo $HOME",
		"! ls",
		// Quoting or escaping that hides a flag.
		`git log "--output=x"`,
		`git log --out\put=x`,
		"git log '--ext-diff'",
		// Expansion that becomes a flag after parsing.
		"git log {--output=x,}",
		"git log *",
		// git flags that write, execute, or reach the network.
		"git log --output=/tmp/x -1",
		"git diff --ext-diff",
		"git diff --textconv",
		"git show --output /tmp/x",
		"git grep -O foo",
		"git grep -nOvim foo",
		"git grep --open-files-in-pager=vim foo",
		"git ls-remote https://example.com/repo.git",
		"git -c core.pager=evil log",
		// git branch and tag forms that create, delete, or move refs.
		"git branch new-branch",
		"git branch -d old",
		"git branch -D old",
		"git branch -vD old",
		"git branch -m new",
		"git branch -mnew",
		"git branch --delete old",
		"git branch --force main HEAD",
		"git branch --set-upstream-to=origin/main",
		"git branch -u origin/main",
		"git branch -- new-branch",
		"git tag v2",
		"git tag -d v1",
		"git tag -a v2 -m msg",
		"git tag -am msg v2",
		"git tag --delete v1",
		"git tag -f v1",
		// git remote forms that mutate or contact the remote.
		"git remote add evil https://example.com",
		"git remote remove origin",
		"git remote set-url origin https://example.com",
		"git remote show origin",
		"git remote prune origin",
		"git remote -v add evil url",
		// date forms that set the clock.
		"date -s 2020-01-01",
		"date -s2020-01-01",
		"date -us 2020-01-01",
		"date --set=2020-01-01",
		"date 0101000020",
		// hostname forms that set the hostname.
		"hostname evil",
		"hostname -F/etc/x",
		"hostname -b evil",
	}
	for _, command := range denied {
		t.Run("deny/"+command, func(t *testing.T) {
			t.Parallel()
			assert.False(t, isSafeReadOnly(command), "isSafeReadOnly(%q)", command)
		})
	}
}

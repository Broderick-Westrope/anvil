package tools

import (
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
	"mvdan.cc/sh/v3/syntax"
)

var safeCommands = []string{
	// Bash builtins and core utils
	"cal",
	"date",
	"df",
	"du",
	"echo",
	"free",
	"groups",
	"hostname",
	"id",
	"ls",
	"printenv",
	"ps",
	"pwd",
	"set",
	"top",
	"type",
	"uname",
	"unset",
	"uptime",
	"whatis",
	"whereis",
	"which",
	"whoami",

	// Git
	"git blame",
	"git branch",
	"git config --get",
	"git config --get-all",
	"git config --get-regexp",
	"git config --get-urlmatch",
	"git config --list",
	"git describe",
	"git diff",
	"git grep",
	"git log",
	"git ls-files",
	"git remote",
	"git rev-parse",
	"git shortlog",
	"git show",
	"git status",
	"git tag",
}

// windowsSafeCommands are safe commands only on Windows.
var windowsSafeCommands = []string{"ipconfig", "systeminfo", "tasklist", "where"}

// safeRedirects are the only redirections a safe command may make. Each
// discards output or merges streams, so none reads or writes a file.
var safeRedirects = []string{">/dev/null", "2>/dev/null", "&>/dev/null", "2>&1", "1>&2", ">&2"}

// plainCall returns the words of command when it parses to one simple
// command that runs nothing else: no chaining, pipes, backgrounding,
// negation, assignments, command, process or arithmetic substitution, and
// no redirection other than safeRedirects.
func plainCall(command string) ([]*syntax.Word, bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return nil, false
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || len(call.Assigns) > 0 ||
		slices.ContainsFunc(stmt.Redirs, isUnsafeRedirect) {
		return nil, false
	}
	substitutes := false
	syntax.Walk(file, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp:
			substitutes = true
		}
		return true
	})
	return call.Args, !substitutes
}

func isUnsafeRedirect(redir *syntax.Redirect) bool {
	target, ok := segment.StaticWord(redir.Word)
	if !ok {
		return true
	}
	var fd string
	if redir.N != nil {
		fd = redir.N.Value
	}
	return !slices.Contains(safeRedirects, fd+redir.Op.String()+target)
}

// safeEntry returns the entry of safe commands for goos that words start
// with and the number of words it spans. Each word of the entry must match
// a whole word with no expansions. Only Windows ignores case, because
// elsewhere a name in another case, such as LS, runs a different file,
// which could be one the repository ships.
func safeEntry(words []*syntax.Word, goos string) (string, int, bool) {
	windows := goos == "windows"
	var names []string
	for _, word := range words {
		name, ok := segment.StaticWord(word)
		if !ok {
			break
		}
		if windows {
			name = strings.ToLower(name)
		}
		names = append(names, name)
	}
	entries := safeCommands
	if windows {
		entries = slices.Concat(safeCommands, windowsSafeCommands)
	}
	for _, entry := range entries {
		fields := strings.Fields(entry)
		if len(fields) <= len(names) && slices.Equal(fields, names[:len(fields)]) {
			return entry, len(fields), true
		}
	}
	return "", 0, false
}

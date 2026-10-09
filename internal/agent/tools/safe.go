package tools

import (
	"runtime"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
)

// safeCommands lists the commands that run without a permission request.
// Each entry is matched word by word against the start of the parsed
// command, so "git config --get" matches only that exact form.
//
// Commands that run another program (env, nice, nohup, time, timeout) or
// act on other processes (kill, killall) are deliberately absent: a
// prefix match on the wrapper says nothing about what it runs. They go
// through the permission service like any other command.
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

// unsafeShellSyntax lists characters and sequences that chain commands,
// substitute commands, redirect input or output, or run in the
// background. A command containing any of them is never fast-pathed. The
// parse in [isSafeReadOnly] rejects them too, but a plain string check
// fails closed even if the parser and the shell ever disagree.
var unsafeShellSyntax = []string{
	";",
	"|",
	"&",
	"$(",
	"`",
	">",
	"<",
	"\n",
	"\r",
}

// containsUnsafeShellSyntax reports whether s contains shell syntax that
// chains, substitutes, redirects, or backgrounds commands.
func containsUnsafeShellSyntax(s string) bool {
	return slices.ContainsFunc(unsafeShellSyntax, func(c string) bool {
		return strings.Contains(s, c)
	})
}

// isSafeReadOnly reports whether command may run without a permission
// request: a single simple command with static arguments that matches a
// [safeCommands] entry and passes that command's argument check.
func isSafeReadOnly(command string) bool {
	if containsUnsafeShellSyntax(command) {
		return false
	}
	argv, ok := segment.SimpleCommand(command)
	if !ok {
		return false
	}
	for _, entry := range safeCommands {
		words := strings.Fields(entry)
		if len(argv) < len(words) || !wordsMatch(argv[:len(words)], words) {
			continue
		}
		return safeArgs(words, argv[len(words):])
	}
	return false
}

func wordsMatch(argv, words []string) bool {
	for i, word := range words {
		if strings.ToLower(argv[i]) != word {
			return false
		}
	}
	return true
}

// safeArgs checks the arguments that follow a matched safeCommands entry.
// Commands with no flag that writes, executes, or reaches the network
// accept any arguments.
func safeArgs(entry, args []string) bool {
	switch entry[0] {
	case "date":
		return safeDateArgs(args)
	case "hostname":
		return allIn(args, "-s", "-f", "-i", "-I", "-d", "-a", "-A",
			"--short", "--fqdn", "--long", "--ip-address", "--all-ip-addresses",
			"--domain", "--alias", "--all-fqdns")
	case "ipconfig":
		return allIn(args, "/all", "/displaydns")
	case "git":
		if hasExpansionChars(args) || hasAnyPrefix(args, gitExecFlags...) {
			return false
		}
		switch entry[1] {
		case "branch":
			return safeRefListArgs(args, "dDmMcCfu",
				[]string{"--delete", "--move", "--copy", "--force", "--set-upstream-to", "--unset-upstream", "--edit-description", "--track", "--no-track", "--create-reflog"},
				[]string{"--list", "-l", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at"})
		case "tag":
			return safeRefListArgs(args, "dasumFfe",
				[]string{"--delete", "--annotate", "--sign", "--local-user", "--message", "--file", "--force", "--edit", "--create-reflog"},
				[]string{"--list", "-l", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at"})
		case "remote":
			return safeGitRemoteArgs(args)
		case "grep":
			return !slices.ContainsFunc(args, func(arg string) bool {
				name, _, _ := strings.Cut(arg, "=")
				return name == "--open-files-in-pager" || isShortCluster(arg) && strings.Contains(arg, "O")
			})
		}
	}
	return true
}

// gitExecFlags run a program named on the command line or by the
// repository's own config, or write to an arbitrary file.
var gitExecFlags = []string{"--output", "--ext-diff", "--textconv"}

// safeDateArgs accepts output formats and display-only flags. Anything
// else, including -s/--set and the MMDDhhmm operand that sets the clock,
// needs a permission request.
func safeDateArgs(args []string) bool {
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "+"):
		case arg == "-u", arg == "--utc", arg == "--universal", arg == "-R", arg == "--rfc-email":
		case strings.HasPrefix(arg, "-I"), strings.HasPrefix(arg, "--iso-8601"), strings.HasPrefix(arg, "--rfc-3339"):
		default:
			return false
		}
	}
	return true
}

// safeRefListArgs accepts git branch/tag invocations that only list refs.
// Short flags are checked character by character so that a denied flag
// clustered with others ("-vd") or carrying its value ("-mfoo") still
// counts. Operands are refused unless a list or filter flag is present,
// since a bare operand names a ref to create.
func safeRefListArgs(args []string, deniedShort string, deniedLong, listFlags []string) bool {
	listing := false
	hasOperand := false
	for _, arg := range args {
		switch {
		case arg == "--":
			hasOperand = true
		case strings.HasPrefix(arg, "--"):
			name, _, _ := strings.Cut(arg, "=")
			if slices.Contains(deniedLong, name) {
				return false
			}
			if slices.Contains(listFlags, name) {
				listing = true
			}
		case isShortCluster(arg):
			if strings.ContainsAny(arg[1:], deniedShort) {
				return false
			}
			if slices.Contains(listFlags, arg) {
				listing = true
			}
		default:
			hasOperand = true
		}
	}
	return !hasOperand || listing
}

// safeGitRemoteArgs accepts listing remotes, get-url, and show -n, which
// reads the local config instead of contacting the remote.
func safeGitRemoteArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "-v", "--verbose":
		return len(args) == 1
	case "get-url":
		return true
	case "show":
		return slices.Contains(args[1:], "-n")
	}
	return false
}

func isShortCluster(arg string) bool {
	return len(arg) > 1 && arg[0] == '-' && arg[1] != '-'
}

// hasExpansionChars reports whether any argument contains a glob or brace
// character. The shell expands these after parsing, so "{--output=x,}" or
// a "*" matching a file named "--output=x" could become a flag the checks
// above never saw.
func hasExpansionChars(args []string) bool {
	return slices.ContainsFunc(args, func(arg string) bool {
		return strings.ContainsAny(arg, "*?[{")
	})
}

func hasAnyPrefix(args []string, prefixes ...string) bool {
	return slices.ContainsFunc(args, func(arg string) bool {
		return slices.ContainsFunc(prefixes, func(prefix string) bool {
			return strings.HasPrefix(arg, prefix)
		})
	})
}

func allIn(args []string, allowed ...string) bool {
	return !slices.ContainsFunc(args, func(arg string) bool {
		return !slices.Contains(allowed, arg)
	})
}

func init() {
	if runtime.GOOS == "windows" {
		safeCommands = append(
			safeCommands,
			// Windows-specific commands
			"ipconfig",
			"nslookup",
			"ping",
			"systeminfo",
			"tasklist",
			"where",
		)
	}
}

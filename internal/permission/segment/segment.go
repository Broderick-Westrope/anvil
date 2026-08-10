// Package segment splits bash commands into individually-evaluable
// simple commands using shell AST parsing.
package segment

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Split parses a bash command and returns each simple command as a
// separate string. Chains (&&, ||, ;), pipes, subshells, command
// substitutions, and process substitutions are all traversed — every
// simple command in the AST becomes a segment. Environment variable
// prefixes are stripped, but redirections are kept so that patterns
// can see file-writing operators like "> /etc/passwd".
//
// Pure assignments with no command (e.g. FOO=bar) produce no
// segments, so a command consisting only of assignments returns an
// empty slice.
//
// If the command cannot be parsed, the entire command is returned as
// a single segment so it can still be evaluated (and will typically
// fall through to the default "ask").
func Split(command string) []string {
	if strings.TrimSpace(command) == "" {
		return []string{command}
	}

	parser := syntax.NewParser()
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return []string{command}
	}

	printer := syntax.NewPrinter()
	seen := make(map[string]struct{})
	var segments []string

	syntax.Walk(file, func(node syntax.Node) bool {
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok {
			return true
		}
		// Skip pure assignments with no command.
		if len(call.Args) == 0 {
			return true
		}

		parts := make([]string, 0, len(call.Args)+len(stmt.Redirs))
		for _, arg := range call.Args {
			var sb strings.Builder
			if err := printer.Print(&sb, arg); err != nil {
				continue
			}
			parts = append(parts, sb.String())
		}
		// Keep redirections visible so exact-match rules cannot be
		// bypassed by writing to files (e.g. "ls > /etc/passwd").
		// The printer does not support Redirect nodes directly, so
		// reassemble from fd, operator, and target word.
		for _, redir := range stmt.Redirs {
			var sb strings.Builder
			if redir.N != nil {
				sb.WriteString(redir.N.Value)
			}
			sb.WriteString(redir.Op.String())
			if redir.Word != nil {
				if err := printer.Print(&sb, redir.Word); err != nil {
					continue
				}
			}
			parts = append(parts, sb.String())
		}
		seg := strings.Join(parts, " ")
		if seg == "" {
			return true
		}
		if _, ok := seen[seg]; !ok {
			seen[seg] = struct{}{}
			segments = append(segments, seg)
		}
		return true
	})

	return segments
}

// subcommandRe matches tokens that look like subcommands: purely
// lowercase alphabetic with optional hyphens (e.g. "commit", "test",
// "rev-parse"), but not flags, paths, or file names.
var subcommandRe = regexp.MustCompile(`^[a-z][a-z-]*$`)

// Generalize converts a concrete command segment into a suggested
// glob pattern for permission grants. The first token is always
// kept; the second token is kept if it looks like a subcommand
// (purely alphabetic with optional hyphens); everything after is
// replaced with " *". The match package treats a trailing " *" as
// optional, so the pattern also matches the bare prefix.
//
//	"git commit -m foo"  → "git commit *"
//	"go test ./..."      → "go test *"
//	"ls -la /tmp"        → "ls *"
//	"cat file.txt"       → "cat *"
//	"pwd"                → "pwd *"
func Generalize(segment string) string {
	tokens := strings.Fields(segment)
	if len(tokens) == 0 {
		return segment
	}

	prefix := tokens[0]
	if len(tokens) > 1 && subcommandRe.MatchString(tokens[1]) {
		prefix += " " + tokens[1]
	}
	return prefix + " *"
}

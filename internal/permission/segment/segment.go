// Package segment splits bash commands into individually-evaluable
// simple commands using shell AST parsing.
package segment

import (
	"cmp"
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// maxUnwrapDepth bounds nested wrapper unwrapping (e.g. "sudo env cmd")
// so a pathological command cannot cause unbounded expansion.
const maxUnwrapDepth = 8

// Split parses a bash command and returns each unit of execution as a
// separate string. Chains (&&, ||, ;), pipes, subshells, command
// substitutions, and process substitutions are all traversed.
//
// Three kinds of segment are produced:
//
//   - Simple commands. Environment variable prefixes are stripped, and
//     redirections that cannot write to a file (fd duplication such as
//     "2>&1", heredocs, and plain "<" reads) are kept inline.
//   - File-writing redirections, emitted as their own segment (for
//     example "> /etc/passwd"). Keeping these separate is what stops a
//     broad rule like "echo *" from also authorising a write to an
//     arbitrary path.
//   - Commands nested inside another command's arguments: the target of
//     a wrapper such as env, sudo, xargs, or timeout, and the body of a
//     find-style -exec/-ok clause.
//   - Normalised spellings of a command or write target. Words without
//     expansions are unquoted and unescaped, brace expansions are
//     expanded, and a command path is reduced to its base name, so
//     "'rm' -rf x", "r”m -rf x", "\rm -rf x", and "/bin/rm -rf x"
//     all also yield "rm -rf x". The original spelling is kept too.
//
// Emitting the nested, redirection, and normalised segments separately
// can only make evaluation stricter, never more permissive, because
// callers combine segment results worst-outcome-first. Words that
// depend on runtime state (variables, command substitution, globs in
// the command name) cannot be resolved and keep their printed form.
//
// Pure assignments with no command (e.g. FOO=bar) produce no segments,
// so a command consisting only of assignments returns an empty slice.
//
// If the command cannot be parsed, the entire command is returned as a
// single segment so it can still be evaluated (and will typically fall
// through to the default "ask").
func Split(command string) []string {
	all, _ := split(command)
	return all
}

// Normalized is like Split but returns only the normalised spelling of
// each segment, so a single command yields one segment however it is
// quoted. Use it to reason about what a command does; use Split for
// permission evaluation.
func Normalized(command string) []string {
	_, normalized := split(command)
	return normalized
}

func split(command string) (all, normalized []string) {
	if strings.TrimSpace(command) == "" {
		return []string{command}, []string{command}
	}

	parser := syntax.NewParser()
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return []string{command}, []string{command}
	}

	printer := syntax.NewPrinter()
	collector := func(out *[]string) func(string) {
		seen := make(map[string]struct{})
		return func(seg string) {
			if seg == "" {
				return
			}
			if _, ok := seen[seg]; ok {
				return
			}
			seen[seg] = struct{}{}
			*out = append(*out, seg)
		}
	}
	addAll, addNormalized := collector(&all), collector(&normalized)

	syntax.Walk(file, func(node syntax.Node) bool {
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}

		// Redirections are collected for every statement, not just
		// simple commands, so that a write hidden behind a subshell
		// such as "(ls) > /etc/passwd" is still surfaced.
		var inline, writes, plainWrites []string
		for _, redir := range stmt.Redirs {
			text, isWrite := formatRedir(printer, redir)
			switch {
			case text == "":
			case isWrite:
				writes = append(writes, text)
				plainWrites = append(plainWrites, cmp.Or(plainRedir(redir), text))
			default:
				inline = append(inline, text)
			}
		}

		var tokens, plain []string
		if call, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			tokens = make([]string, 0, len(call.Args))
			for _, arg := range call.Args {
				var sb strings.Builder
				if err := printer.Print(&sb, arg); err != nil {
					continue
				}
				tokens = append(tokens, sb.String())
				plain = append(plain, plainWords(arg, sb.String())...)
			}
		}
		if len(plain) > 0 && strings.Contains(plain[0], "/") {
			plain[0] = path.Base(plain[0])
		}
		join := func(words []string) string {
			if len(words) == 0 {
				return ""
			}
			return strings.Join(append(slices.Clone(words), inline...), " ")
		}

		addAll(join(tokens))
		addAll(join(plain))
		addNormalized(join(plain))
		for i := range writes {
			addAll(writes[i])
			addAll(plainWrites[i])
			addNormalized(plainWrites[i])
		}
		for _, nested := range nestedCommands(tokens) {
			addAll(nested)
		}
		for _, nested := range nestedCommands(plain) {
			addAll(nested)
			addNormalized(nested)
		}
		return true
	})

	return all, normalized
}

// plainWords returns the words arg expands to when it has no runtime
// expansions, or the printed form when it does. Brace expansion can turn
// one word into several.
func plainWords(arg *syntax.Word, printed string) []string {
	words := []*syntax.Word{arg}
	if strings.Contains(printed, "{") {
		clone := &syntax.Word{Parts: slices.Clone(arg.Parts)}
		if syntax.SplitBraces(clone) {
			words = expand.Braces(clone)
		}
	}
	out := make([]string, 0, len(words))
	for _, w := range words {
		value, ok := staticWord(w)
		if !ok {
			return []string{printed}
		}
		out = append(out, value)
	}
	return out
}

// staticWord returns the value a word expands to when it contains only
// literal text and quoting. It reports false for any word whose value
// depends on runtime state.
func staticWord(w *syntax.Word) (string, bool) {
	var sb strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			sb.WriteString(unescape(p.Value, false))
		case *syntax.SglQuoted:
			if !p.Dollar {
				sb.WriteString(p.Value)
				continue
			}
			value, _, err := expand.Format(nil, p.Value, nil)
			if err != nil {
				return "", false
			}
			sb.WriteString(value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				sb.WriteString(unescape(lit.Value, true))
			}
		default:
			return "", false
		}
	}
	return sb.String(), true
}

// unescape removes shell backslash escapes. Inside double quotes only
// $, `, ", \, and newline can be escaped.
func unescape(s string, quoted bool) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			sb.WriteByte(s[i])
			continue
		}
		next := s[i+1]
		if quoted && !strings.ContainsRune("$`\"\\\n", rune(next)) {
			sb.WriteByte(s[i])
			continue
		}
		i++
		if next != '\n' {
			sb.WriteByte(next)
		}
	}
	return sb.String()
}

// plainRedir renders a file-writing redirection with its target
// unquoted, or returns "" when the target has runtime expansions.
func plainRedir(redir *syntax.Redirect) string {
	if redir.Word == nil {
		return ""
	}
	target, ok := staticWord(redir.Word)
	if !ok {
		return ""
	}
	var fd string
	if redir.N != nil {
		fd = redir.N.Value
	}
	return strings.TrimSpace(fd + redir.Op.String() + " " + target)
}

// fdTargetRe matches a redirection target that names a file descriptor
// rather than a file: "1", "2-", or "-" (close).
var fdTargetRe = regexp.MustCompile(`^[0-9]+-?$|^-$`)

// formatRedir renders a redirection and reports whether it can write to
// a file. File-writing redirections are rendered with a space after the
// operator ("> /tmp/out") so that patterns can target the path
// readably; others keep the compact shell form ("2>&1").
func formatRedir(printer *syntax.Printer, redir *syntax.Redirect) (string, bool) {
	var target string
	if redir.Word != nil {
		var sb strings.Builder
		if err := printer.Print(&sb, redir.Word); err != nil {
			return "", false
		}
		target = sb.String()
	}

	var fd string
	if redir.N != nil {
		fd = redir.N.Value
	}
	op := redir.Op.String()

	if isWriteRedir(redir.Op, target) {
		return strings.TrimSpace(fd + op + " " + target), true
	}
	return fd + op + target, false
}

// isWriteRedir reports whether op creates, truncates, or appends to a
// file. ">&" is ambiguous: it duplicates a descriptor when the target
// is a number, but redirects both streams to a file otherwise.
func isWriteRedir(op syntax.RedirOperator, target string) bool {
	switch op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut,
		syntax.RdrClob, syntax.AppClob,
		syntax.RdrAll, syntax.RdrAllClob,
		syntax.AppAll, syntax.AppAllClob:
		return true
	case syntax.DplOut:
		return !fdTargetRe.MatchString(target)
	default:
		return false
	}
}

// wrapperCommands run another command supplied in their arguments. The
// wrapped command is emitted as its own segment so that allowing the
// wrapper does not implicitly allow everything it can launch. The triage
// package relies on this list (via IsWrapper) to never propose allow
// rules for wrappers.
var wrapperCommands = map[string]struct{}{
	"command": {},
	"doas":    {},
	"env":     {},
	"exec":    {},
	"ionice":  {},
	"nice":    {},
	"nohup":   {},
	"setsid":  {},
	"stdbuf":  {},
	"sudo":    {},
	"time":    {},
	"timeout": {},
	"watch":   {},
	"xargs":   {},
}

// IsWrapper reports whether cmd runs another command supplied in its
// arguments.
func IsWrapper(cmd string) bool {
	_, ok := wrapperCommands[cmd]
	return ok
}

// execFlags introduce a nested command in find-style utilities.
var execFlags = map[string]struct{}{
	"-exec":    {},
	"-execdir": {},
	"-ok":      {},
	"-okdir":   {},
}

// execTerminators end a find-style -exec clause.
var execTerminators = map[string]struct{}{
	";":   {},
	`\;`:  {},
	"';'": {},
	`";"`: {},
	"+":   {},
}

// numericRe matches a bare number, used to skip wrapper operands such
// as the duration in "timeout 5 cmd".
var numericRe = regexp.MustCompile(`^[0-9]+$`)

// nestedCommands returns commands hidden inside the arguments of tokens:
// the target of a wrapper command and the body of any -exec clause.
//
// Wrapper unwrapping skips the wrapper itself plus any leading
// assignments, flags, and bare numbers. This is a heuristic: a flag that
// takes a separate value (as in "sudo -u root cmd") leaves the value at
// the front of the result. That yields a segment matching no rule, so
// the command falls through to "ask" — stricter than the alternative,
// never more permissive.
func nestedCommands(tokens []string) []string {
	var out []string
	if inner := unwrap(tokens, 0); len(inner) > 0 {
		out = append(out, strings.Join(inner, " "))
	}
	for _, clause := range execClauses(tokens) {
		out = append(out, clause)
	}
	return out
}

// unwrap resolves nested wrapper commands down to the innermost target.
func unwrap(tokens []string, depth int) []string {
	if depth >= maxUnwrapDepth || len(tokens) == 0 {
		return nil
	}
	if _, ok := wrapperCommands[path.Base(tokens[0])]; !ok {
		return nil
	}

	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		if strings.HasPrefix(tok, "-") || numericRe.MatchString(tok) || isAssignment(tok) {
			continue
		}
		inner := tokens[i:]
		if deeper := unwrap(inner, depth+1); len(deeper) > 0 {
			return deeper
		}
		return inner
	}
	return nil
}

// isAssignment reports whether tok is a NAME=value environment prefix
// rather than a command or path.
func isAssignment(tok string) bool {
	eq := strings.Index(tok, "=")
	if eq <= 0 {
		return false
	}
	name := tok[:eq]
	for i := 0; i < len(name); i++ {
		c := name[i]
		isAlpha := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if !isAlpha && !(isDigit && i > 0) {
			return false
		}
	}
	return true
}

// execClauses extracts the command bodies of any find-style -exec, -ok,
// -execdir, or -okdir clauses in tokens.
func execClauses(tokens []string) []string {
	var out []string
	for i := 0; i < len(tokens); i++ {
		if _, ok := execFlags[tokens[i]]; !ok {
			continue
		}
		var body []string
		for j := i + 1; j < len(tokens); j++ {
			if _, done := execTerminators[tokens[j]]; done {
				i = j
				break
			}
			body = append(body, tokens[j])
			i = j
		}
		if len(body) > 0 {
			out = append(out, strings.Join(body, " "))
		}
	}
	return out
}

// subcommandRe matches tokens that look like subcommands: purely
// lowercase alphabetic with optional hyphens (e.g. "commit", "test",
// "rev-parse"), but not flags, paths, or file names.
var subcommandRe = regexp.MustCompile(`^[a-z][a-z-]*$`)

// redirPrefixRe matches a segment that is a file-writing redirection.
var redirPrefixRe = regexp.MustCompile(`^[0-9]*(&>>|&>|>>\||>>|>\||>&|<>|>)`)

// IsRedirect reports whether a segment is a file-writing redirection.
func IsRedirect(seg string) bool {
	return redirPrefixRe.MatchString(strings.TrimSpace(seg))
}

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
//
// Redirection segments are returned unchanged, so approving a write to
// one path never grants writes to every path:
//
//	"> /tmp/out.txt"     → "> /tmp/out.txt"
func Generalize(segment string) string {
	if IsRedirect(segment) {
		return segment
	}

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

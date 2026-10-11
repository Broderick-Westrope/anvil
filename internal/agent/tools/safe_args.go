package tools

import (
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
	"mvdan.cc/sh/v3/syntax"
)

// safeArgs holds the rules for safe commands that are read-only only with
// some arguments, keyed by their safeCommands entry. A rule gets the
// arguments after the entry and reports whether they keep the command
// read-only.
var safeArgs = map[string]func(args []string) bool{
	"date":         readsDate,
	"hostname":     onlyFlags(hostnameReadFlags),
	"git branch":   listsRefs(branchReadFlags),
	"git tag":      listsRefs(tagReadFlags),
	"git remote":   readsRemotes,
	"git blame":    noArgWithPrefix("--output"),
	"git diff":     noArgWithPrefix("--output"),
	"git log":      noArgWithPrefix("--output"),
	"git shortlog": noArgWithPrefix("--output"),
	"git show":     noArgWithPrefix("--output"),
	"git grep":     opensNoPager,
}

// hasSafeArgs reports whether words, the arguments after the safe command
// entry, keep to what the entry allows. Git commands and commands with a
// rule must spell every argument out, since a variable, glob or brace
// expansion could turn into any flag when the shell runs it.
func hasSafeArgs(entry string, words []*syntax.Word) bool {
	rule, ok := safeArgs[entry]
	if !ok && !strings.HasPrefix(entry, "git ") {
		return true
	}
	args := make([]string, 0, len(words))
	for _, word := range words {
		arg, static := segment.StaticWord(word)
		if !static || expands(word) {
			return false
		}
		args = append(args, arg)
	}
	return !ok || rule(args)
}

// expands reports whether the shell would expand word into other words,
// through brace expansion or an unquoted glob.
func expands(word *syntax.Word) bool {
	braces := &syntax.Word{Parts: slices.Clone(word.Parts)}
	syntax.SplitBraces(braces)
	return slices.ContainsFunc(braces.Parts, func(part syntax.WordPart) bool {
		_, ok := part.(*syntax.BraceExp)
		return ok
	}) || slices.ContainsFunc(word.Parts, func(part syntax.WordPart) bool {
		lit, ok := part.(*syntax.Lit)
		return ok && strings.ContainsAny(lit.Value, "*?[")
	})
}

// flagSet holds flags, either exactly or as a prefix of a flag with a
// value.
type flagSet struct {
	exact    []string
	prefixes []string
}

func (f flagSet) allows(arg string) bool {
	return slices.Contains(f.exact, arg) || slices.ContainsFunc(f.prefixes, func(prefix string) bool {
		return strings.HasPrefix(arg, prefix)
	})
}

// onlyFlags returns the rule for a command that is read-only when every
// argument is one of flags.
func onlyFlags(flags flagSet) func(args []string) bool {
	return func(args []string) bool {
		return !slices.ContainsFunc(args, func(arg string) bool {
			return !flags.allows(arg)
		})
	}
}

var hostnameReadFlags = flagSet{
	exact: []string{"-f", "-s", "-i", "-I", "-d", "-a", "-A", "--fqdn", "--short", "--domain"},
}

// dateDisplayFlags are the date arguments that only change what date
// prints. Anything else, such as -s or a bare date, sets the clock.
var dateDisplayFlags = flagSet{
	exact:    []string{"-u", "--utc", "--universal", "-R", "--date", "--reference"},
	prefixes: []string{"+", "-d", "--date=", "-r", "--reference=", "-I", "--rfc-", "--iso-8601"},
}

// dateValueFlags take the next argument as their value.
var dateValueFlags = []string{"-d", "--date", "-r", "--reference"}

func readsDate(args []string) bool {
	for i := 0; i < len(args); i++ {
		if !dateDisplayFlags.allows(args[i]) {
			return false
		}
		if slices.Contains(dateValueFlags, args[i]) {
			i++
		}
	}
	return true
}

var branchReadFlags = flagSet{
	exact: []string{
		"-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "-l", "--list",
		"--show-current", "--no-color",
	},
	prefixes: []string{"--sort=", "--format=", "--color", "--column"},
}

var tagReadFlags = flagSet{
	exact:    []string{"-l", "--list"},
	prefixes: []string{"-n", "--sort=", "--format=", "--column", "--color"},
}

// refFilterFlags make git branch and git tag list refs, so a commit they
// take as the next argument reads as a pattern, which is just as safe.
var refFilterFlags = []string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at"}

// listsRefs returns the rule for git branch or git tag, which create a ref
// named by a positional argument unless a list or filter flag makes it a
// pattern.
func listsRefs(flags flagSet) func(args []string) bool {
	return func(args []string) bool {
		listing, named := false, false
		for _, arg := range args {
			if !strings.HasPrefix(arg, "-") {
				named = true
				continue
			}
			flag, _, _ := strings.Cut(arg, "=")
			if slices.Contains(refFilterFlags, flag) {
				listing = true
				continue
			}
			if !flags.allows(arg) {
				return false
			}
			listing = listing || arg == "-l" || arg == "--list"
		}
		return listing || !named
	}
}

// readsRemotes is the rule for git remote: listing remotes, get-url, or
// show with -n, which stops it querying the remote.
func readsRemotes(args []string) bool {
	subcommand := slices.IndexFunc(args, func(arg string) bool {
		return arg != "-v" && arg != "--verbose"
	})
	return subcommand < 0 || args[subcommand] == "get-url" ||
		args[subcommand] == "show" && slices.Contains(args[subcommand+1:], "-n")
}

func noArgWithPrefix(prefixes ...string) func(args []string) bool {
	return func(args []string) bool {
		return !slices.ContainsFunc(args, func(arg string) bool {
			return slices.ContainsFunc(prefixes, func(prefix string) bool {
				return strings.HasPrefix(arg, prefix)
			})
		})
	}
}

// opensNoPager is the rule for git grep, whose -O and
// --open-files-in-pager run a command on the matching files. Short flags
// can be bundled, so any short flag containing an o or O counts,
// including --only-matching's -o.
func opensNoPager(args []string) bool {
	return !slices.ContainsFunc(args, func(arg string) bool {
		if strings.HasPrefix(arg, "--") {
			return strings.HasPrefix(arg, "--op")
		}
		return strings.HasPrefix(arg, "-") && strings.ContainsAny(arg, "oO")
	})
}

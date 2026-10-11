package tools

import (
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
)

// safeArgs holds the rules for safe commands that are read-only only with
// some arguments, keyed by their safeCommands entry. A rule gets the
// lower-cased arguments after the entry and reports whether they keep the
// command read-only.
var safeArgs = map[string]func(args []string) bool{
	"git branch": listsRefs(branchReadFlags),
	"git tag":    listsRefs(tagReadFlags),
	"git remote": readsRemotes,
	"git diff":   noArgWithPrefix("--output"),
	"git log":    noArgWithPrefix("--output"),
	"git show":   noArgWithPrefix("--output"),
	"git grep":   opensNoPager,
	// Git accepts abbreviated long options, so a prefix catches them all.
	// --exec is a hidden alias of --upload-pack.
	"git ls-remote": noArgWithPrefix("--u", "--exe"),
}

// hasSafeArgs reports whether command, which starts with the safe command
// entry, keeps to the arguments its rule allows. The arguments are judged
// as the shell will pass them, so quoting cannot disguise one.
func hasSafeArgs(command, entry string) bool {
	rule, ok := safeArgs[entry]
	if !ok {
		return true
	}
	segments := segment.Normalized(command)
	// A $ left after normalising expands at run time into any argument.
	if len(segments) != 1 || strings.Contains(segments[0], "$") {
		return false
	}
	args, found := strings.CutPrefix(strings.ToLower(segments[0])+" ", entry+" ")
	return found && rule(strings.Fields(args))
}

// refReadFlags are the flags that keep git branch or git tag listing refs,
// either exactly or as a prefix of a flag with a value.
type refReadFlags struct {
	exact    []string
	prefixes []string
}

func (f refReadFlags) allows(arg string) bool {
	return slices.Contains(f.exact, arg) || slices.ContainsFunc(f.prefixes, func(prefix string) bool {
		return strings.HasPrefix(arg, prefix)
	})
}

var branchReadFlags = refReadFlags{
	exact: []string{
		"-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "-l", "--list",
		"--show-current", "--merged", "--no-merged", "--contains", "--no-contains",
		"--no-color", "--points-at",
	},
	prefixes: []string{"--sort=", "--format=", "--color", "--column"},
}

var tagReadFlags = refReadFlags{
	exact: []string{
		"-l", "--list", "--contains", "--no-contains", "--merged", "--no-merged",
		"--points-at",
	},
	prefixes: []string{"-n", "--sort=", "--format=", "--column", "--color"},
}

// listsRefs returns the rule for git branch or git tag, which create a ref
// named by a positional argument unless a list flag makes it a pattern.
func listsRefs(flags refReadFlags) func(args []string) bool {
	return func(args []string) bool {
		listing, named := false, false
		for _, arg := range args {
			if !strings.HasPrefix(arg, "-") {
				named = true
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

// readsRemotes is the rule for git remote: listing remotes, or the show
// and get-url subcommands.
func readsRemotes(args []string) bool {
	subcommand := slices.IndexFunc(args, func(arg string) bool {
		return arg != "-v" && arg != "--verbose"
	})
	return subcommand < 0 || args[subcommand] == "show" || args[subcommand] == "get-url"
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
// can be bundled and arguments are lower-cased, so any short flag
// containing an o counts, including --only-matching's -o.
func opensNoPager(args []string) bool {
	return !slices.ContainsFunc(args, func(arg string) bool {
		if strings.HasPrefix(arg, "--") {
			return strings.HasPrefix(arg, "--op")
		}
		return strings.HasPrefix(arg, "-") && strings.Contains(arg, "o")
	})
}

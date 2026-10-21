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
	"date":       readsDate,
	"hostname":   onlyFlags(hostnameReadFlags),
	"ipconfig":   onlyFlags(ipconfigReadFlags),
	"git branch": listsRefs(branchReadFlags),
	"git tag":    listsRefs(tagReadFlags),
	"git remote": readsRemotes,
	"git diff":   comparesWorkspacePaths,
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
	if slices.ContainsFunc(args, gitFileFlags[entry].match) {
		return false
	}
	return !ok || rule(args)
}

// expands reports whether the shell would expand word into other words,
// through brace expansion or an unquoted glob.
func expands(word *syntax.Word) bool {
	braces := &syntax.Word{Parts: slices.Clone(word.Parts)}
	syntax.SplitBraces(braces)
	return slices.ContainsFunc(braces.Parts, isBraceExpansion) ||
		slices.ContainsFunc(word.Parts, isUnquotedGlob)
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

// ipconfigReadFlags are the ipconfig switches that only print. Others,
// such as /release, /renew and /flushdns, change the network state.
var ipconfigReadFlags = flagSet{exact: []string{"/all", "/displaydns"}}

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
// show with -n, which stops it querying the remote. The -n must come
// before the first remote, and there must be no --, after which -n would
// name a remote rather than set the flag.
func readsRemotes(args []string) bool {
	subcommand := slices.IndexFunc(args, func(arg string) bool {
		return arg != "-v" && arg != "--verbose"
	})
	if subcommand < 0 || args[subcommand] == "get-url" {
		return true
	}
	if args[subcommand] != "show" || slices.Contains(args, "--") {
		return false
	}
	options := args[subcommand+1:]
	remote := slices.IndexFunc(options, isOperand)
	if remote >= 0 {
		options = options[:remote]
	}
	return slices.Contains(options, "-n")
}

// fileFlags are the flags of a git command that name a file to read or
// write, or a command to run, which could be anywhere on disk.
type fileFlags struct {
	long []longFlag
	// short holds the letters of short flags. Git lets short flags be
	// bundled, so any short flag argument containing one matches.
	short string
}

// longFlag is a long flag and the shortest abbreviation of it git accepts.
// Commands that take no abbreviations use the whole flag for both.
type longFlag struct {
	name, abbrev string
}

func (f fileFlags) match(arg string) bool {
	if strings.HasPrefix(arg, "--") {
		flag, _, _ := strings.Cut(arg, "=")
		return slices.ContainsFunc(f.long, func(long longFlag) bool {
			return strings.HasPrefix(flag, long.abbrev) && strings.HasPrefix(long.name, flag)
		})
	}
	return strings.HasPrefix(arg, "-") && strings.ContainsAny(arg, f.short)
}

var (
	outputFlag = longFlag{"--output", "--output"}
	// logFlags are the file flags of git log and git show. -O names a file
	// that orders the files in a diff.
	logFlags    = fileFlags{long: []longFlag{outputFlag}, short: "O"}
	configFlags = fileFlags{long: []longFlag{{"--file", "--fil"}}, short: "f"}
)

// gitFileFlags holds the file flags of each safe git command, keyed by its
// safeCommands entry. The view tool asks before reading outside the working
// directory, and a safe command must not get around that.
var gitFileFlags = map[string]fileFlags{
	"git blame": {
		long:  []longFlag{outputFlag, {"--contents", "--con"}, {"--ignore-revs-file", "--ignore-revs"}},
		short: "S",
	},
	"git diff":     {long: []longFlag{outputFlag, {"--no-index", "--no-index"}}, short: "O"},
	"git log":      logFlags,
	"git show":     logFlags,
	"git shortlog": {long: []longFlag{outputFlag}},
	// The --o abbreviation is ambiguous, so git rejects it, but it is kept
	// out with the rest.
	"git grep": {long: []longFlag{{"--open-files-in-pager", "--o"}}, short: "Of"},
	"git ls-files": {
		long:  []longFlag{{"--exclude-from", "--exclude-f"}, {"--exclude-per-directory", "--exclude-p"}},
		short: "X",
	},
	"git config --get":          configFlags,
	"git config --get-all":      configFlags,
	"git config --get-regexp":   configFlags,
	"git config --get-urlmatch": configFlags,
	"git config --list":         configFlags,
}

// comparesWorkspacePaths is the rule for git diff, which compares any two
// files on disk, as --no-index does, when one is outside the repository.
// It rejects arguments that are absolute paths, start from a home directory
// or climb out with "..". A flag is caught only when its value has a ".."
// element, and then the command just asks.
func comparesWorkspacePaths(args []string) bool {
	return !slices.ContainsFunc(args, func(arg string) bool {
		elements := strings.FieldsFunc(arg, func(r rune) bool { return r == '/' || r == '\\' })
		return strings.IndexAny(arg, `/\~`) == 0 || strings.Index(arg, ":") == 1 || slices.Contains(elements, "..")
	})
}

func isBraceExpansion(part syntax.WordPart) bool {
	_, ok := part.(*syntax.BraceExp)
	return ok
}

func isUnquotedGlob(part syntax.WordPart) bool {
	lit, ok := part.(*syntax.Lit)
	return ok && strings.ContainsAny(lit.Value, "*?[")
}

func isOperand(arg string) bool {
	return !strings.HasPrefix(arg, "-")
}

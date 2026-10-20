// Package branchsummary prepares an abandoned conversation branch for a
// summarizer agent: it finds the abandoned range, extracts a ledger of
// what the branch's tool calls did, serialises the branch within a token
// budget, and finishes the agent's output for storage.
package branchsummary

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/bouncer"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

const (
	// maxListEntries caps each file list and the command list, in the
	// prompt and in the stored summary.
	maxListEntries = 50
	maxFocusChars  = 2_000
	// maxEarlierSummaryChars caps each compaction or branch summary
	// inside the range.
	maxEarlierSummaryChars = 4_000
	maxToolInputChars      = 500
	maxToolResultChars     = 2_000
	// maxSummaryChars caps the agent's output before the file lists are
	// appended, and maxStoredChars caps the stored summary as a whole.
	maxSummaryChars = 12_000
	maxStoredChars  = 16_000
	// maxEntryChars caps one ledger entry: a path or a command.
	maxEntryChars = 300
	// maxMessageChars caps one serialised message, so a long final report
	// can't crowd out the rest of the conversation.
	maxMessageChars = 8_000
	// charsPerToken is a conservative estimate: code and paths run under
	// four characters a token.
	charsPerToken = 3
)

// Abandoned returns the messages on fromPath after its deepest common
// ancestor with targetPath, oldest first. Both paths run root first.
func Abandoned(fromPath, targetPath []message.Message) []message.Message {
	shared := 0
	for shared < len(fromPath) && shared < len(targetPath) && fromPath[shared].ID == targetPath[shared].ID {
		shared++
	}
	return slices.Clone(fromPath[shared:])
}

// Worthwhile reports whether the range holds an assistant reply, so a
// summary could say something. A rewind that drops only the user's own
// message isn't worth summarising.
func Worthwhile(rng []message.Message) bool {
	return slices.ContainsFunc(rng, func(m message.Message) bool {
		return m.Role == message.Assistant && isConversation(m) &&
			(strings.TrimSpace(m.Content().Text) != "" || len(m.ToolCalls()) > 0)
	})
}

func isConversation(m message.Message) bool {
	return m.MessageType == message.MessageTypeMessage || m.MessageType == ""
}

// gitOptions matches git's global options before its subcommand, such as
// -C <dir>, which worktree workflows use for nearly every command.
const gitOptions = `((?:\s+(?:-C\s+(?:"[^"]*"|'[^']*'|\S+)|-c\s+\S+|--[\w-]+(?:=\S+)?))*)`

var (
	stateChangingCommand = regexp.MustCompile(
		`\bgit` + gitOptions + `\s+(commit|push|merge|rebase|reset|cherry-pick|tag|checkout\s+-b|switch\s+-c)\b` +
			`|\bgh\s+pr\s+(create|edit|merge|comment|close|ready)\b` +
			`|\bwtp\s+(add|remove)\b`)
	// compoundCommand matches a command that runs more than one step;
	// looseChain one whose exit status can hide a failed step.
	compoundCommand = regexp.MustCompile(`&&|\|\||;|\||\n`)
	looseChain      = regexp.MustCompile(`\|\||;|\||\n`)
	gitDirOption    = regexp.MustCompile(`-C\s+(?:"([^"]*)"|'([^']*)'|(\S+))`)
)

var (
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	gitSHARecord = regexp.MustCompile(`(?m)^\[[^\]\s]+(?: \([^)]*\))? ([0-9a-f]{7,40})\]` +
		`|\b([0-9a-f]{7,40})\.\.\.?([0-9a-f]{7,40})\b` +
		`|^HEAD is now at ([0-9a-f]{7,40})\b`)
	prURLPattern  = regexp.MustCompile(`https://[^\s"')]+/pull/\d+`)
	permissionErr = "Permission denied"
	// failedOutput matches the lines bash adds to a failed command's
	// output, for results recorded before bash stored exit codes.
	failedOutput = regexp.MustCompile(`(?m)^(Exit code [1-9]\d*|Command was aborted before completion)$`)
)

// Ledger extracts what the range's tool calls did. children returns the
// messages of the session a tool call (in the message with messageID)
// delegated to, or nil; their effects
// are folded in where the call happened. Ledgers of branch summaries in
// the range carry forward.
func Ledger(rng []message.Message, children func(messageID string, call message.ToolCall) []message.Message) message.BranchLedger {
	var b ledgerBuilder
	b.add(rng, children)
	return b.ledger()
}

type ledgerBuilder struct {
	modified, read []string
	events         []message.LedgerEvent
}

func (b *ledgerBuilder) add(rng []message.Message, children func(messageID string, call message.ToolCall) []message.Message) {
	results := map[string]message.ToolResult{}
	for _, m := range rng {
		for _, r := range m.ToolResults() {
			results[r.ToolCallID] = r
		}
	}
	for _, m := range rng {
		if m.MessageType == message.MessageTypeBranchSummary {
			b.carryForward(m)
			continue
		}
		for _, call := range m.ToolCalls() {
			res, hasResult := results[call.ID]
			b.addCall(m.ID, call, res, hasResult, children)
		}
	}
}

// carryForward folds in the ledger of an earlier branch summary.
func (b *ledgerBuilder) carryForward(m message.Message) {
	for _, p := range m.Parts {
		if s, ok := p.(message.BranchSummaryContent); ok {
			b.modified = append(b.modified, s.Ledger.Modified...)
			b.read = append(b.read, s.Ledger.Read...)
			b.events = append(b.events, s.Ledger.Events...)
		}
	}
}

// addCall records one tool call's effect, by tool.
func (b *ledgerBuilder) addCall(messageID string, call message.ToolCall, res message.ToolResult, hasResult bool, children func(messageID string, call message.ToolCall) []message.Message) {
	succeeded := hasResult && !res.IsError
	switch call.Name {
	case tools.ViewToolName:
		if path := inputField(call, "file_path"); succeeded && path != "" {
			b.read = append(b.read, path)
		}
	case tools.EditToolName, tools.MultiEditToolName, tools.WriteToolName:
		if path := inputField(call, "file_path"); succeeded && path != "" {
			b.modified = append(b.modified, path)
		}
	case tools.BashToolName:
		if command := inputField(call, "command"); stateChangingCommand.MatchString(command) {
			b.events = append(b.events, message.LedgerEvent{
				Tool:    call.Name,
				Command: truncate(redact(command), maxEntryChars),
				Dir:     commandDir(command, inputField(call, "working_dir")),
				Status:  commandStatus(command, res, hasResult),
				Refs:    refs(res.Content),
			})
		}
	default:
		if child := children(messageID, call); child != nil {
			b.add(child, children)
		}
	}
}

func (b *ledgerBuilder) ledger() message.BranchLedger {
	modified := sortedUnique(b.modified)
	var read []string
	for _, p := range sortedUnique(b.read) {
		if !slices.Contains(modified, p) {
			read = append(read, p)
		}
	}
	return message.BranchLedger{Modified: modified, Read: read, Events: b.events}
}

func sortedUnique(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}

// commandDir is the directory a git command acted in: its -C option if it
// has one, otherwise the directory it ran in.
func commandDir(command, workingDir string) string {
	m := stateChangingCommand.FindStringSubmatch(command)
	if m == nil || m[1] == "" {
		return workingDir
	}
	if d := gitDirOption.FindStringSubmatch(m[1]); d != nil {
		return d[1] + d[2] + d[3]
	}
	return workingDir
}

// commandStatus is status for a single command. A shell's exit status
// covers its whole chain, so it can't say how one git step in a compound
// command went: such a command is unknown unless an && chain succeeded
// as a whole.
func commandStatus(command string, res message.ToolResult, hasResult bool) message.LedgerStatus {
	st := status(res, hasResult)
	if !compoundCommand.MatchString(command) {
		return st
	}
	if st == message.LedgerSucceeded && !looseChain.MatchString(command) {
		return st
	}
	if st == message.LedgerDenied {
		return st
	}
	return message.LedgerUnknown
}

func status(res message.ToolResult, hasResult bool) message.LedgerStatus {
	if !hasResult {
		return message.LedgerUnknown
	}
	if res.IsError {
		if strings.HasPrefix(res.Content, permissionErr) {
			return message.LedgerDenied
		}
		return message.LedgerFailed
	}
	var meta struct {
		Background  bool   `json:"background"`
		ShellID     string `json:"shell_id"`
		ExitCode    int    `json:"exit_code"`
		Interrupted bool   `json:"interrupted"`
	}
	_ = json.Unmarshal([]byte(res.Metadata), &meta)
	switch {
	case meta.Background && meta.ShellID != "":
		return message.LedgerUnknown
	case meta.ExitCode != 0, meta.Interrupted, failedOutput.MatchString(res.Content):
		return message.LedgerFailed
	default:
		return message.LedgerSucceeded
	}
}

// refs returns the commit SHAs git printed in its own records (a commit
// line, a push or fetch range, a reset), and the pull request URLs gh
// printed. Other hex strings in the output are left out, since a compound
// command can print anything.
func refs(output string) []string {
	var out []string
	for _, m := range gitSHARecord.FindAllStringSubmatch(output, -1) {
		for _, sha := range m[1:] {
			if sha != "" {
				out = append(out, sha)
			}
		}
	}
	out = append(out, prURLPattern.FindAllString(output, -1)...)
	return slices.Compact(out)
}

func inputField(call message.ToolCall, field string) string {
	var in map[string]any
	if json.Unmarshal([]byte(call.Input), &in) != nil {
		return ""
	}
	s, _ := in[field].(string)
	return s
}

// Prompt builds the summarizer's task prompt from the range, its ledger
// and the user's focus text, keeping the whole prompt within budget
// tokens where it can. The framing, focus and ledger always fit first;
// then earlier summaries, newest first; then messages, newest first.
// Secrets are redacted on a best-effort basis.
func Prompt(rng []message.Message, ledger message.BranchLedger, focus string, budget int) string {
	// Every piece is redacted whole before it's cut to size, so a cut can't
	// split a secret the patterns would have matched.
	keep := ledgerRefs(ledger)
	clean := func(s string) string { return redactKeeping(s, keep) }
	var summaries, conversation []string
	for _, m := range rng {
		switch m.MessageType {
		case message.MessageTypeCompaction, message.MessageTypeBranchSummary:
			if s := earlierSummary(m, clean); s != "" {
				summaries = append(summaries, s)
			}
		case message.MessageTypeMessage, "":
			if s := serialise(m, clean); s != "" {
				conversation = append(conversation, endLine(truncate(s, maxMessageChars)))
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("Summarise the abandoned conversation branch below for the conversation it branched from. ")
	sb.WriteString("The ledger was extracted from the branch's tool calls in code: it records what was attempted and observed, not verified current state.\n\n")
	var ledgerText strings.Builder
	writeLedger(&ledgerText, ledger)
	// The ledger may take at most half the budget, so the conversation
	// always gets the rest.
	sb.WriteString(truncate(clean(ledgerText.String()), budget*charsPerToken/2))
	if focus = strings.TrimSpace(focus); focus != "" {
		fmt.Fprintf(&sb, "<focus>\n%s\n</focus>\n\n", truncate(clean(focus), maxFocusChars))
	}
	remaining := budget*charsPerToken - sb.Len()

	var kept []string
	for i := len(summaries) - 1; i >= 0; i-- {
		if len(summaries[i]) > remaining {
			break
		}
		remaining -= len(summaries[i])
		kept = append(kept, summaries[i])
	}
	if len(kept) > 0 {
		sb.WriteString("<earlier-summaries>\n")
		for i := len(kept) - 1; i >= 0; i-- {
			sb.WriteString(kept[i])
		}
		sb.WriteString("</earlier-summaries>\n\n")
	}

	const omittedMarker = "[%d earlier messages omitted]\n"
	remaining -= len("<conversation>\n</conversation>\n") + len(omittedMarker) + 8
	first := len(conversation)
	for first > 0 && len(conversation[first-1]) <= remaining {
		first--
		remaining -= len(conversation[first])
	}
	if first == len(conversation) && first > 0 && remaining > 0 {
		// Even the newest message doesn't fit whole; keep its start.
		first--
		conversation[first] = endLine(truncate(conversation[first], remaining))
	}
	sb.WriteString("<conversation>\n")
	if first > 0 {
		fmt.Fprintf(&sb, omittedMarker, first)
	}
	for _, s := range conversation[first:] {
		sb.WriteString(s)
	}
	sb.WriteString("</conversation>\n")
	return clean(sb.String())
}

func writeLedger(sb *strings.Builder, l message.BranchLedger) {
	sb.WriteString("<ledger>\n")
	writeList(sb, "Modified files", l.Modified)
	writeList(sb, "Files read", l.Read)
	if len(l.Events) > 0 {
		sb.WriteString("Commands, in order:\n")
		// The latest commands say what state the branch left, so the
		// oldest are the ones dropped.
		events := l.Events
		if omitted := len(events) - maxListEntries; omitted > 0 {
			fmt.Fprintf(sb, "[%d earlier commands omitted]\n", omitted)
			events = events[omitted:]
		}
		for _, e := range events {
			fmt.Fprintf(sb, "- [%s] %s", e.Status, e.Command)
			if e.Dir != "" {
				fmt.Fprintf(sb, " (in %s)", e.Dir)
			}
			if len(e.Refs) > 0 {
				fmt.Fprintf(sb, " refs: %s", strings.Join(e.Refs, ", "))
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("</ledger>\n\n")
}

func writeList(sb *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	sb.WriteString(title + ":\n")
	for i, item := range items {
		if i == maxListEntries {
			fmt.Fprintf(sb, "+%d more\n", len(items)-i)
			return
		}
		sb.WriteString("- " + item + "\n")
	}
}

func earlierSummary(m message.Message, clean func(string) string) string {
	for _, p := range m.Parts {
		switch s := p.(type) {
		case message.CompactionContent:
			return fmt.Sprintf("<summary source=\"compaction of history up to this point, which may predate the branch\">\n%s\n</summary>\n",
				truncate(clean(s.Summary), maxEarlierSummaryChars))
		case message.BranchSummaryContent:
			return fmt.Sprintf("<summary source=\"an earlier branch this branch came back from\">\n%s\n</summary>\n",
				truncate(clean(s.Summary), maxEarlierSummaryChars))
		}
	}
	return ""
}

func serialise(m message.Message, clean func(string) string) string {
	var sb strings.Builder
	for _, p := range m.Parts {
		switch p := p.(type) {
		case message.TextContent:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			role := "Assistant"
			if m.Role == message.User {
				role = "User"
			}
			fmt.Fprintf(&sb, "[%s]: %s\n", role, clean(p.Text))
		case message.ToolCall:
			fmt.Fprintf(&sb, "[Tool call] %s: %s\n", p.Name, truncate(redactedInput(p.Input, clean), maxToolInputChars))
		case message.ToolResult:
			label := "Tool result"
			if p.IsError {
				label = "Tool error"
			}
			fmt.Fprintf(&sb, "[%s] %s\n", label, truncate(clean(p.Content), maxToolResultChars))
		}
	}
	return sb.String()
}

// redactedInput redacts a tool input's decoded string values, where
// quoting can't hide a secret from the patterns, and re-encodes it with
// sorted keys, so the same call serialises to the same bytes.
func redactedInput(input string, clean func(string) string) string {
	var v any
	if json.Unmarshal([]byte(input), &v) != nil {
		return clean(input)
	}
	b, err := json.Marshal(redactValues(v, clean))
	if err != nil {
		return clean(input)
	}
	return string(b)
}

func redactValues(v any, clean func(string) string) any {
	switch v := v.(type) {
	case string:
		return clean(v)
	case map[string]any:
		for k, item := range v {
			v[k] = redactValues(item, clean)
		}
	case []any:
		for i, item := range v {
			v[i] = redactValues(item, clean)
		}
	}
	return v
}

func endLine(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func truncate(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "…[truncated]"
}

// Finalize prepares the agent's output for storage: it redacts it, caps
// its length and appends the ledger's file lists verbatim, so they reach
// later turns exactly as extracted.
func Finalize(summary string, ledger message.BranchLedger) string {
	summary = strings.TrimSpace(redactKeeping(summary, ledgerRefs(ledger)))
	if len(summary) > maxSummaryChars {
		summary = summary[:maxSummaryChars] + "\n[summary truncated]"
	}
	var sb strings.Builder
	sb.WriteString(summary)
	writeTagged(&sb, "modified-files", ledger.Modified)
	writeTagged(&sb, "read-files", ledger.Read)
	return truncate(sb.String(), maxStoredChars)
}

func writeTagged(sb *strings.Builder, tag string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(sb, "\n\n<%s>\n", tag)
	for i, item := range items {
		if i == maxListEntries {
			fmt.Fprintf(sb, "+%d more\n", len(items)-i)
			break
		}
		sb.WriteString(truncate(redact(item), maxEntryChars) + "\n")
	}
	fmt.Fprintf(sb, "</%s>", tag)
}

func redact(s string) string { return bouncer.Redact(s) }

// redactKeeping redacts s but keeps the given refs: commit SHAs the
// ledger took from git's own output, which the generic long-run rule
// would otherwise remove.
func redactKeeping(s string, keep []string) string {
	placeholders := make([]string, 0, 2*len(keep))
	for i, ref := range keep {
		placeholder := fmt.Sprintf("\x00%d\x00", i)
		s = regexp.MustCompile(`\b`+ref+`\b`).ReplaceAllLiteralString(s, placeholder)
		placeholders = append(placeholders, placeholder, ref)
	}
	return strings.NewReplacer(placeholders...).Replace(redact(s))
}

// ledgerRefs returns the ledger's commit SHAs, longest first so a short
// SHA never splits a longer one.
func ledgerRefs(l message.BranchLedger) []string {
	var refs []string
	for _, e := range l.Events {
		for _, r := range e.Refs {
			if shaPattern.MatchString(r) && !slices.Contains(refs, r) {
				refs = append(refs, r)
			}
		}
	}
	slices.SortFunc(refs, func(a, b string) int { return len(b) - len(a) })
	return refs
}

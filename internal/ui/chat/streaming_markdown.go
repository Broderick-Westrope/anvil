package chat

import (
	"strings"

	"charm.land/glamour/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
)

// streamingMarkdown caches a "stable prefix" glamour render so each
// streaming flush only re-renders the trailing portion of the
// document. F8 of docs/notes/2026-05-12-chat-rendering-perf.md.
//
// The boundary between "stable" and "trailing" is detected by
// [findSafeMarkdownBoundary]: a position immediately after a blank
// line at which we can prove no markdown construct is open
// (fenced code block, list, table, block quote, setext header).
//
// Two renders concatenated are NOT generally equal to a single
// render of the whole document — glamour's wrap state is reset
// between calls. The boundary check is therefore deliberately
// conservative; whenever it has the slightest doubt the call
// falls back to a full render and the cache is left untouched.
//
// Invariants:
//
//   - stablePrefix is always a literal byte prefix of the most
//     recently rendered content. If a new content does not have
//     stablePrefix as its prefix the cache is dropped.
//   - stablePrefixRender is the glamour render of stablePrefix
//     alone, with surrounding whitespace trimmed for clean
//     concatenation.
//   - width is the glamour wrap width that produced
//     stablePrefixRender. A width change drops the cache.
type streamingMarkdown struct {
	width              int
	stablePrefix       string
	stablePrefixRender string
	// Cached cumulative state at the stable prefix boundary.
	// Used by findBoundaryAfter to validate new boundary candidates
	// without re-scanning the entire prefix from the start.
	// baseFenceCount is even when the boundary is a real safe
	// boundary and odd when the prefix was force-advanced inside an
	// open fence (openFence != "").
	baseFenceCount    int
	baseHasListMarker bool
	// openFence is the fence opener line (e.g. "```go") active at
	// the stable-prefix boundary when the prefix was force-advanced
	// mid-fence, or "" when the boundary sits outside any fence.
	// When set, trailing renders prepend it so the trail keeps its
	// code-block styling, and the cached prefix render was produced
	// with a synthetic closing fence appended.
	openFence string
	// forced records that at least one force-advance happened since
	// the last Reset. The glued output then contains synthetic fence
	// seams (a long code block split into two adjacent blocks), so
	// the final post-stream render should be a clean monolithic
	// render — see RenderFinal.
	forced bool
}

const (
	// maxUnsafeTrailBytes is the largest segment Render will push
	// through glamour in a single streaming flush. Without this cap
	// a document that never reaches a safe boundary — most commonly
	// a long open code fence — forces a full re-render of the whole
	// accumulated text on every flush: O(n) glamour work at ~30
	// flushes/sec with ~1000x allocation amplification. That is the
	// mechanism behind the multi-GB memory blowups observed during
	// concurrent subagent review streams. When the unsafe segment
	// exceeds this cap the stable prefix is force-advanced through
	// it (splitting open fences with a synthetic close/reopen).
	maxUnsafeTrailBytes = 8 * 1024

	// forcedTrailKeepBytes is how much trailing content a
	// force-advance leaves behind as the fresh-render segment. Kept
	// well under maxUnsafeTrailBytes so consecutive flushes don't
	// immediately re-trigger the force-advance.
	forcedTrailKeepBytes = 2 * 1024
)

// Reset drops every cached field. After Reset the next Render call
// is guaranteed to be a full render.
func (s *streamingMarkdown) Reset() {
	s.width = 0
	s.stablePrefix = ""
	s.stablePrefixRender = ""
	s.baseFenceCount = 0
	s.baseHasListMarker = false
	s.openFence = ""
	s.forced = false
}

// Render returns the glamour render of content at the given width,
// reusing the cached stable-prefix render when it is safe to do so.
// On any uncertainty the call falls back to a full render via
// renderer and leaves the cache untouched (or drops it).
//
// The returned string has its trailing newline trimmed to match
// the existing renderMarkdown contract on AssistantMessageItem.
//
// Concurrency: glamour's Render is stateful and not safe for
// concurrent invocation on a shared renderer. Crush's TUI is
// single-threaded so production never contends, but parallel
// callers (most notably the test suite) must serialize. We hold
// [common.LockMarkdownRenderer] for the entire prefix +
// trailing render sequence so other goroutines cannot interleave
// their own Render calls and corrupt goldmark's BlockStack.
func (s *streamingMarkdown) Render(content string, width int, renderer *glamour.TermRenderer) string {
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	defer mu.Unlock()
	full := func() string {
		out, err := renderer.Render(content)
		if err != nil {
			return content
		}
		return strings.TrimSuffix(out, "\n")
	}

	// Width change OR content not a prefix-extension: drop cache,
	// full render, optionally try to seed a fresh boundary on this
	// call (step "f" in the design note).
	if width != s.width || !strings.HasPrefix(content, s.stablePrefix) {
		s.Reset()
		s.width = width
		out := full()
		s.tryAdvanceFromEmpty(content, width, renderer)
		// Whatever remains after seeding (possibly everything, e.g. a
		// giant open code fence with no safe boundary) must not exceed
		// the cap, or the next flush repeats this full render.
		if len(content)-len(s.stablePrefix) > maxUnsafeTrailBytes {
			s.forceAdvance(content, renderer)
		}
		return out
	}

	// Incremental boundary search: only scan the delta after the
	// stable prefix. The cached cumulative state (baseFenceCount,
	// baseHasListMarker) lets us validate candidates in O(delta)
	// instead of re-scanning the entire prefix (upstream 884391f9).
	boundary := s.findBoundaryAfter(content)
	if boundary < 0 {
		// No safe boundary anywhere yet. If the document is still
		// small, full-render and wait for a boundary; once it grows
		// past the cap, force-advance so per-flush work stays
		// bounded.
		if len(content) <= maxUnsafeTrailBytes {
			return full()
		}
		s.forceAdvance(content, renderer)
		if s.stablePrefix == "" {
			// Force-advance found no line boundary to cut at
			// (single giant line). Nothing bounded we can do.
			return full()
		}
		trail := content[len(s.stablePrefix):]
		return glueRenders(s.stablePrefixRender, s.renderTrailingCtx(trail, renderer))
	}

	if boundary <= len(s.stablePrefix) {
		// Cached prefix already covers an at-least-as-late
		// boundary. Render the trailing partial fresh and glue.
		// When the trail has grown past the cap (open fence, giant
		// paragraph), force-advance through it first so the fresh
		// render stays bounded.
		if len(content)-len(s.stablePrefix) > maxUnsafeTrailBytes {
			s.forceAdvance(content, renderer)
		}
		trail := content[len(s.stablePrefix):]
		return glueRenders(s.stablePrefixRender, s.renderTrailingCtx(trail, renderer))
	}

	// boundary > len(stablePrefix): we have a NEW chunk of safe
	// content. Render the new chunk, append to stablePrefixRender,
	// promote the boundary, then render the remaining trail.
	newChunk := content[len(s.stablePrefix):boundary]
	newChunkRender := s.renderTrailingCtx(newChunk, renderer)
	s.stablePrefixRender = glueRenders(s.stablePrefixRender, newChunkRender)
	// Update cumulative state for the new stable prefix. A safe
	// boundary implies even fence parity, so any force-advanced
	// open fence has closed inside newChunk.
	s.baseFenceCount += countFenceLines(newChunk)
	s.baseHasListMarker = s.baseHasListMarker || chunkHasListMarker(newChunk, s.openFence != "")
	s.stablePrefix = content[:boundary]
	s.openFence = ""

	trail := content[boundary:]
	if trail == "" {
		// boundary == len(content): no trailing content. Returning
		// the cached prefix render directly is correct.
		return s.stablePrefixRender
	}
	// The trail past the new boundary can itself be oversized (safe
	// boundary followed by a giant open fence); keep it bounded.
	if len(trail) > maxUnsafeTrailBytes {
		s.forceAdvance(content, renderer)
		trail = content[len(s.stablePrefix):]
	}
	return glueRenders(s.stablePrefixRender, s.renderTrailingCtx(trail, renderer))
}

// RenderFinal returns the definitive render for a completed stream:
// one monolithic glamour render of the full document. It drops any
// streaming cache state (including force-advance seams) and does not
// re-seed it — the stream is complete, so no further incremental
// flushes will occur and seeding would only waste renders.
func (s *streamingMarkdown) RenderFinal(content string, width int, renderer *glamour.TermRenderer) string {
	s.Reset()
	s.width = width
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	defer mu.Unlock()
	out, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSuffix(out, "\n")
}

// forceAdvance promotes the stable prefix through an unsafe segment
// so per-flush render work stays bounded. It cuts at a line boundary
// that leaves ~forcedTrailKeepBytes of trailing content, renders the
// promoted chunk with synthetic fence context (reopening the fence
// active at the old boundary, closing any fence still open at the
// cut), and records the fence opener active at the new boundary so
// subsequent trailing renders keep their code-block styling.
//
// The glued output is visually near-identical to the monolithic
// render — a long code block shows a seam at the cut — and the seam
// is removed by the clean render in RenderFinal once the stream
// completes. Callers must hold the renderer lock.
func (s *streamingMarkdown) forceAdvance(content string, renderer *glamour.TermRenderer) {
	cutLimit := len(content) - forcedTrailKeepBytes
	if cutLimit <= len(s.stablePrefix) {
		return
	}
	cut := cutLimit
	if nl := strings.LastIndexByte(content[:cutLimit], '\n'); nl >= len(s.stablePrefix) {
		cut = nl + 1
	}
	if len(content)-cut > maxUnsafeTrailBytes {
		// The newline cut left an oversized trail (a giant line —
		// minified JSON, base64 — dominates the document). Cut at
		// the raw byte offset instead; the mid-line seam is removed
		// by RenderFinal.
		cut = cutLimit
	}
	newChunk := content[len(s.stablePrefix):cut]

	// Track fence state across the promoted chunk, starting from
	// the state at the old boundary.
	inFence := s.openFence != ""
	opener := s.openFence
	for line := range splitLines(newChunk) {
		if isFenceLine(line) {
			inFence = !inFence
			if inFence {
				opener = line
			} else {
				opener = ""
			}
		}
	}

	src := newChunk
	if s.openFence != "" {
		src = s.openFence + "\n" + src
	}
	if inFence {
		// Ensure the synthetic closer lands on its own line — a
		// byte-offset cut may leave newChunk without a trailing
		// newline.
		if !strings.HasSuffix(src, "\n") {
			src += "\n"
		}
		src += closingFence(opener)
	}

	out := s.renderTrailing(src, renderer)
	s.stablePrefixRender = glueRenders(s.stablePrefixRender, out)
	s.baseFenceCount += countFenceLines(newChunk)
	s.baseHasListMarker = s.baseHasListMarker || chunkHasListMarker(newChunk, s.openFence != "")
	s.stablePrefix = content[:cut]
	if inFence {
		s.openFence = opener
	} else {
		s.openFence = ""
	}
	s.forced = true
}

// closingFence returns a fence line that closes the fence opened by
// opener: the same fence character with at least as long a run.
func closingFence(opener string) string {
	i := 0
	for i < len(opener) && i < 3 && opener[i] == ' ' {
		i++
	}
	if i >= len(opener) || (opener[i] != '`' && opener[i] != '~') {
		return "```"
	}
	c := opener[i]
	run := 0
	for i < len(opener) && opener[i] == c {
		i++
		run++
	}
	return strings.Repeat(string(c), max(run, 3))
}

// renderTrailingCtx renders a trailing segment that starts at the
// stable-prefix boundary, reopening the force-advanced fence when one
// is active so the segment keeps its code-block styling.
func (s *streamingMarkdown) renderTrailingCtx(text string, renderer *glamour.TermRenderer) string {
	if text == "" {
		return ""
	}
	if s.openFence != "" {
		return s.renderTrailing(s.openFence+"\n"+text, renderer)
	}
	return s.renderTrailing(text, renderer)
}

// tryAdvanceFromEmpty seeds the cache from a fresh state. We've
// already paid the cost of a full render of `content`; if there is
// a safe boundary inside it, render the prefix once more (cheap
// relative to the full render we just did) and cache it so the
// next flush can avoid the full work.
//
// This is the optional optimisation step "f" from the design
// note. We render the prefix separately rather than try to
// recover it from the full render output because two renders
// concatenated ≠ a single render of the whole, and we prefer the
// cached prefix render to be byte-for-byte what we'd produce on a
// future cached call.
func (s *streamingMarkdown) tryAdvanceFromEmpty(content string, width int, renderer *glamour.TermRenderer) {
	boundary := findSafeMarkdownBoundary(content)
	if boundary <= 0 {
		return
	}
	prefix := content[:boundary]
	out, err := renderer.Render(prefix)
	if err != nil {
		return
	}
	s.stablePrefix = prefix
	s.stablePrefixRender = trimGlamourMargins(out)
	s.width = width
	// Seed cumulative state for incremental boundary search.
	s.baseFenceCount = countFenceLines(prefix)
	s.baseHasListMarker = chunkHasListMarker(prefix, false)
}

// findBoundaryAfter searches for the latest safe boundary in content
// that is strictly after the stable prefix. It uses the cached
// cumulative state (baseFenceCount, baseHasListMarker) to validate
// candidates without re-scanning the entire prefix, making the search
// O(delta) instead of O(n) per tick (upstream 884391f9).
//
// Returns -1 when no safe boundary exists after the stable prefix.
func (s *streamingMarkdown) findBoundaryAfter(content string) int {
	// When there is no stable prefix, fall back to the full scan.
	if len(s.stablePrefix) == 0 {
		return findSafeMarkdownBoundary(content)
	}

	// Scan blank-line candidates from latest to earliest, but only
	// those strictly after the stable prefix. Candidates at or
	// before the stable prefix are already covered by the cache.
	for p := blankLineBefore(content, len(content)); p > len(s.stablePrefix); p = blankLineBefore(content, p-1) {
		if s.isSafeBoundaryIncremental(content, p) {
			return p
		}
	}
	// No new boundary found after the stable prefix. Return the
	// stable prefix length so the caller takes the "boundary <=
	// len(stablePrefix)" path (render trailing fresh, keep cache).
	return len(s.stablePrefix)
}

// isSafeBoundaryIncremental validates a boundary candidate at
// position p using the cached cumulative state plus a delta scan
// of content[len(stablePrefix):p]. This avoids the O(n) re-scan
// that isSafeBoundaryAt performs on every candidate.
func (s *streamingMarkdown) isSafeBoundaryIncremental(content string, p int) bool {
	delta := content[len(s.stablePrefix):p]

	// (2) Fence parity: base count + delta count must be even.
	if (s.baseFenceCount+countFenceLines(delta))%2 != 0 {
		return false
	}

	// (2b) HTML and link-ref hazards in the delta. The delta starts
	// inside a fence when the prefix was force-advanced mid-fence.
	if deltaHasHTMLorRef(delta, s.openFence != "") {
		return false
	}

	// Both the list hazard (2b) and the open-construct check (3) need
	// the last non-blank line before the boundary. Compute the backward
	// scan once; it walks the full prefix and this is the per-delta hot
	// path.
	lastLine := lastNonBlankLine(content[:p])

	// (2b) List hazard: if a list marker exists anywhere in the
	// full prefix (base OR delta), the last non-blank line before
	// the boundary must not be an indented continuation paragraph.
	hasListMarker := s.baseHasListMarker || chunkHasListMarker(delta, s.openFence != "")
	if hasListMarker && lastLine != "" && !isListItemMarker(strings.TrimLeft(lastLine, " \t")) {
		if lastLine[0] == ' ' || lastLine[0] == '\t' {
			return false
		}
	}

	// (3) Last non-blank line must not open a construct.
	if lastLine != "" && lineOpensConstruct(lastLine) {
		return false
	}

	// (4) Setext underline check.
	if rest := content[p:]; rest != "" {
		first := firstNonBlankLine(rest)
		if isSetextUnderlineCandidate(first) {
			return false
		}
	}

	return true
}

// deltaHasHTMLorRef reports whether the delta (text between the
// stable prefix and a boundary candidate) contains an HTML block
// opener or a link reference definition.
func deltaHasHTMLorRef(delta string, startInFence bool) bool {
	inFence := startInFence
	for line := range splitLines(delta) {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isHTMLBlockOpener(line) || isLinkRefDefinition(line) {
			return true
		}
	}
	return false
}

// chunkHasListMarker reports whether any line in chunk is a
// list-item marker (outside fenced code blocks). startInFence is
// true when the chunk begins inside an open fenced block (i.e. the
// stable prefix was force-advanced mid-fence).
func chunkHasListMarker(chunk string, startInFence bool) bool {
	inFence := startInFence
	for line := range splitLines(chunk) {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if isListItemMarker(strings.TrimLeft(line, " \t")) {
			return true
		}
	}
	return false
}

// renderTrailing renders a trailing partial as a fresh glamour
// document and trims the surrounding whitespace so it can be
// concatenated to a cached prefix render without doubled blank
// lines.
func (s *streamingMarkdown) renderTrailing(text string, renderer *glamour.TermRenderer) string {
	if text == "" {
		return ""
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return trimGlamourMargins(out)
}

// glueRenders concatenates two glamour-rendered fragments with a
// single blank line separator. Glamour outputs typically carry
// their own surrounding margins; trimming on both sides and
// gluing with "\n\n" prevents the visible double-margin seam.
//
// Empty fragments are tolerated so the same helper works for the
// "boundary == len(content)" path where there is no trailing
// segment.
func glueRenders(prefix, trail string) string {
	prefix = trimGlamourMargins(prefix)
	trail = trimGlamourMargins(trail)
	switch {
	case prefix == "" && trail == "":
		return ""
	case prefix == "":
		return trail
	case trail == "":
		return prefix
	default:
		return prefix + "\n\n" + trail
	}
}

// trimGlamourMargins strips leading and trailing whitespace
// (including newlines) from a glamour-rendered fragment.
// Glamour adds a leading blank line for documents that open with
// a heading or paragraph, plus a trailing newline; both must be
// removed before concatenation.
func trimGlamourMargins(s string) string {
	return strings.Trim(s, " \t\n")
}

// findSafeMarkdownBoundary returns the byte offset of the END of
// the latest safe boundary in content, i.e. the offset such that
// content[:boundary] is a valid stable-prefix candidate. The
// returned offset always points immediately after a blank-line
// separator, so concatenating a fresh render of content[boundary:]
// to a cached render of content[:boundary] does not require glamour
// to share state across the cut.
//
// Returns -1 when no safe boundary exists. SAFETY FIRST: any time
// we have the slightest doubt we return -1 and let the caller fall
// back to a full render.
//
// Decision tree, in order of preference (latest boundary wins):
//
//  1. Walk backward through every "blank line" position p such that
//     content[:p] ends with "\n\n" (or "\n[ \t]*\n").
//  2. For each candidate, check that content[:p] has an even
//     number of triple-backtick fence lines (no open fenced
//     block). Any odd count means we'd be cutting inside a fence
//     and mis-syntax-highlighting the trailing partial.
//     2b. Reject if any line in content[:p] (outside fenced blocks)
//     is a list-marker line, an HTML-block opener, or a link
//     reference definition. See [prefixHasOpenHazard] for the
//     reasoning behind these "anywhere in prefix" rejects.
//  3. Reject if the last non-blank line of content[:p] is:
//     - a list item marker line ("^\s*([-*+]|\d+\.)\s")
//     - a table line (contains "|")
//     - a block quote ("^\s*>")
//     - a setext header underline ("^=+\s*$" or "^-+\s*$")
//     - an indented code line (4+ leading spaces or a tab)
//  4. Reject if the line immediately AFTER the boundary (skipping
//     leading blank lines) looks like a setext underline (a line
//     of '=' or '-' only). Rendering the prefix as a paragraph
//     would change once the underline arrived; that's exactly the
//     "splitting changes the prefix render" hazard §4.4 calls out.
//
// Returns the byte offset of the first character AFTER the blank
// line, i.e. the start of the trailing segment.
func findSafeMarkdownBoundary(content string) int {
	if len(content) == 0 {
		return -1
	}

	// Iterate every blank-line position from latest to earliest.
	for p := blankLineBefore(content, len(content)); p > 0; p = blankLineBefore(content, p-1) {
		if !isSafeBoundaryAt(content, p) {
			continue
		}
		return p
	}
	return -1
}

// blankLineBefore returns the byte offset of the first character
// AFTER the latest blank-line separator that ends strictly before
// `until`. A blank-line separator is a sequence "\n([ \t]*\n)+"
// — one newline, then one or more lines containing only spaces or
// tabs and terminated by another newline. The returned offset is
// the start of the first non-blank line that follows the
// separator (or the position immediately after the final newline,
// if no further content remains).
//
// Returns -1 when no blank-line separator exists before `until`.
func blankLineBefore(content string, until int) int {
	if until <= 0 {
		return -1
	}
	// Walk backward looking for a newline followed (after optional
	// blank-line content) by another newline. We track the latest
	// newline we've seen; if the next earlier newline has only
	// blank chars between them, we have a blank-line separator
	// and the boundary sits immediately after the latest newline.
	end := until
	for end > 0 {
		nl := strings.LastIndexByte(content[:end], '\n')
		if nl < 0 {
			return -1
		}
		// Look for an earlier newline whose gap to nl is empty
		// or whitespace only.
		prev := strings.LastIndexByte(content[:nl], '\n')
		for prev >= 0 {
			gap := content[prev+1 : nl]
			if isBlankOrSpaces(gap) {
				return nl + 1
			}
			// Gap had non-whitespace; nl is not a blank-line
			// separator. Move up: try with the earlier newline as
			// the new "nl" candidate.
			break
		}
		end = nl
	}
	return -1
}

// isBlankOrSpaces reports whether s consists entirely of spaces
// and tabs (or is empty).
func isBlankOrSpaces(s string) bool {
	for i := range len(s) {
		if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return true
}

// isSafeBoundaryAt reports whether content[:p] is a safe stable
// prefix. p must be a blank-line boundary (start of a line, with a
// blank line immediately preceding).
//
// Beyond the last-line checks, three "anywhere in the prefix"
// hazards force a reject because they cannot be reliably reasoned
// about by inspecting the trailing line alone. For each of these
// the simplest, safest rule was chosen — see prefixHasOpenHazard.
func isSafeBoundaryAt(content string, p int) bool {
	prefix := content[:p]

	// (2) Even number of triple-backtick fence lines.
	if countFenceLines(prefix)%2 != 0 {
		return false
	}

	// (2b) Anywhere-in-prefix hazards: open list (B1), HTML block
	// opener (B2), reference link definition (B3). Any of these
	// anywhere in the prefix forces a fallback.
	if prefixHasOpenHazard(prefix) {
		return false
	}

	// (3) Inspect the last non-blank line of the prefix.
	lastLine := lastNonBlankLine(prefix)
	if lastLine != "" && lineOpensConstruct(lastLine) {
		return false
	}

	// (4) If anything follows, make sure it doesn't look like a
	// setext underline that would retroactively turn the last
	// paragraph of the prefix into a header.
	if rest := content[p:]; rest != "" {
		first := firstNonBlankLine(rest)
		if isSetextUnderlineCandidate(first) {
			return false
		}
	}

	return true
}

// prefixHasOpenHazard reports whether prefix contains any of three
// constructs that cannot be safely cut at a blank-line boundary
// even when the immediately preceding line looks fine. Each check
// uses the SIMPLEST viable conservative rule per the F8 round-2
// review:
//
//	B1 (loose lists). A loose list has a blank line between an item
//	   and a continuation paragraph that begins with indentation
//	   but no list marker. If a candidate boundary lands on that
//	   blank line, the prefix's trailing non-blank line is the
//	   continuation paragraph, NOT a list marker, so the last-line
//	   check would accept it even though the list is still open.
//
//	   Rule chosen: any list-marker line ANYWHERE in the prefix
//	   forces -1. This is overly conservative — it forfeits
//	   boundary advancement past a closed list — but it eliminates
//	   the entire bug class with zero parsing of CommonMark's
//	   loose-list closure semantics. We retain the most useful
//	   boundary in practice: the one BEFORE the list opens (no
//	   marker has appeared in the prefix yet).
//
//	B2 (HTML blocks). CommonMark defines seven HTML-block opener
//	   patterns (script/pre/style/textarea, comments, processing
//	   instructions, CDATA, declarations, recognised tag names).
//	   If the prefix opens an HTML block that the suffix closes,
//	   splitting renders the prefix as raw HTML and the suffix as
//	   prose.
//
//	   Rule chosen: any HTML-block opener anywhere in the prefix
//	   forces -1. Same trade-off as B1 — the typical assistant
//	   output contains no raw HTML, so the perf cost is zero in
//	   the common case.
//
//	B3 (reference link definitions). A line of the form
//	   "[label]: <url>" defines a link reference that the suffix
//	   may later use as "[text][label]". Splitting the document
//	   loses the definition because each half is rendered as an
//	   independent glamour document.
//
//	   Rule chosen: any reference link definition line anywhere in
//	   the prefix forces -1. Suffix-side reference detection is
//	   fragile (three syntaxes: [text][label], [label][], [label]),
//	   so the prefix-side check is the simpler safe choice.
//
// All three rules accept the perf hit of "no boundary after a
// list / HTML block / link def" in exchange for guaranteed
// soundness. If profiling shows this kills the F8 win on real
// streaming traces, the next iteration can promote each rule to
// its less-conservative variant (closure-aware list tracking,
// per-tag HTML close detection, suffix-aware ref tracking).
// prefixHasOpenHazard reports whether prefix contains any of three
// constructs that cannot be safely cut at a blank-line boundary
// even when the immediately preceding line looks fine.
//
//	B1 (loose lists, refined). A loose list has a blank line between
//	   an item and a continuation paragraph that begins with
//	   indentation but no list marker. If a candidate boundary lands
//	   on that blank line, the prefix's trailing non-blank line is the
//	   continuation paragraph, NOT a list marker, so the last-line
//	   check in lineOpensConstruct would accept it even though the
//	   list is still open.
//
//	   Rule chosen: track whether any list-marker line appears in the
//	   prefix. If one does AND the last non-blank line is indented
//	   (but is not itself a list marker), the list is potentially
//	   open and we reject. This catches loose-list continuation
//	   paragraphs (indented, no marker) that lineOpensConstruct
//	   misses (it only flags 4+ spaces), without forfeiting every
//	   boundary after a CLOSED list. A list followed by a blank line
//	   and then a non-indented paragraph is closed; the boundary
//	   after that paragraph is safe.
//
//	   The previous rule rejected on any list marker anywhere in the
//	   prefix, which killed the streaming cache for every document
//	   that ever contained a list — the dominant case for LLM
//	   thinking blocks (upstream 884391f9).
//
//	B2 (HTML blocks). CommonMark defines seven HTML-block opener
//	   patterns (script/pre/style/textarea, comments, processing
//	   instructions, CDATA, declarations, recognised tag names).
//	   If the prefix opens an HTML block that the suffix closes,
//	   splitting renders the prefix as raw HTML and the suffix as
//	   prose.
//
//	   Rule chosen: any HTML-block opener anywhere in the prefix
//	   forces -1. Same trade-off as B1 — the typical assistant
//	   output contains no raw HTML, so the perf cost is zero in
//	   the common case.
//
//	B3 (reference link definitions). A line of the form
//	   "[label]: <url>" defines a link reference that the suffix
//	   may later use as "[text][label]". Splitting the document
//	   loses the definition because each half is rendered as an
//	   independent glamour document.
//
//	   Rule chosen: any reference link definition line anywhere in
//	   the prefix forces -1. Suffix-side reference detection is
//	   fragile (three syntaxes: [text][label], [label][], [label]),
//	   so the prefix-side check is the simpler safe choice.
func prefixHasOpenHazard(prefix string) bool {
	inFence := false
	hasListMarker := false
	var lastNonBlankTrimmed string
	var lastNonBlankRaw string
	for line := range splitLines(prefix) {
		// Track fenced state so list/html/ref patterns inside a
		// fenced code block do not falsely trigger the hazards.
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		lastNonBlankTrimmed = trimmed
		lastNonBlankRaw = line
		// B1: track list markers.
		if isListItemMarker(trimmed) {
			hasListMarker = true
		}
		// B2: HTML block opener.
		if isHTMLBlockOpener(line) {
			return true
		}
		// B3: link reference definition.
		if isLinkRefDefinition(line) {
			return true
		}
	}
	// B1 (refined): a list is potentially open only when a marker
	// appeared earlier AND the last non-blank line is indented but
	// is not itself a list marker (i.e. it is a continuation
	// paragraph that lineOpensConstruct would miss because it only
	// catches 4+ leading spaces). A non-indented last line means
	// the list was closed by the blank line before it.
	if hasListMarker && lastNonBlankTrimmed != "" && !isListItemMarker(lastNonBlankTrimmed) {
		if len(lastNonBlankRaw) > 0 && (lastNonBlankRaw[0] == ' ' || lastNonBlankRaw[0] == '\t') {
			return true
		}
	}
	return false
}

// countFenceLines counts lines that begin a fenced code block in
// the CommonMark sense: a line whose first non-whitespace run is
// at least three consecutive backticks (or tildes). Each such
// line toggles the fenced state, so an even count means every
// opened fence has been closed.
//
// We accept up to three leading spaces of indentation (CommonMark
// rule) and require the fence characters to be the FIRST
// non-whitespace content of the line. We deliberately do NOT
// attempt to parse info-strings or differentiate opener from
// closer beyond toggling — a closing fence is just any line
// whose first non-whitespace run is >=3 of the same fence char.
func countFenceLines(s string) int {
	n := 0
	for line := range splitLines(s) {
		if isFenceLine(line) {
			n++
		}
	}
	return n
}

// isFenceLine reports whether line opens or closes a fenced code
// block.
func isFenceLine(line string) bool {
	// Strip up to 3 spaces of indentation.
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) {
		return false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return false
	}
	run := 0
	for i < len(line) && line[i] == c {
		i++
		run++
	}
	return run >= 3
}

// lastNonBlankLine returns the last non-blank line of s, or ""
// when every line is blank.
func lastNonBlankLine(s string) string {
	last := ""
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			last = line
		}
	}
	return last
}

// firstNonBlankLine returns the first non-blank line of s, or ""
// when every line is blank.
func firstNonBlankLine(s string) string {
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// splitLines yields the lines of s without their terminators. The
// final segment is yielded even if not newline-terminated.
func splitLines(s string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				if !yield(s[start:i]) {
					return
				}
				start = i + 1
			}
		}
		if start <= len(s)-1 {
			yield(s[start:])
		}
	}
}

// lineOpensConstruct reports whether line keeps a markdown
// construct open across the boundary. We err conservatively —
// any case that smells like list/table/quote/setext/indented-code
// returns true.
func lineOpensConstruct(line string) bool {
	// Indented code: a tab, or 4+ leading spaces.
	if len(line) > 0 && line[0] == '\t' {
		return true
	}
	if strings.HasPrefix(line, "    ") {
		return true
	}

	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}

	// Block quote.
	if trimmed[0] == '>' {
		return true
	}

	// List item: "- " "* " "+ " or "<digits>. " or "<digits>) ".
	if isListItemMarker(trimmed) {
		return true
	}

	// Table: any pipe character anywhere in the line. Conservative:
	// pipe-in-prose is rare and the cost of bailing is one slow
	// frame.
	if strings.ContainsRune(line, '|') {
		return true
	}

	// Setext underline candidate as the LAST line of the prefix:
	// this would be a setext header for an even-earlier paragraph.
	// Refuse to split at all in this case — the boundary is right
	// in the middle of a header.
	if isSetextUnderlineCandidate(trimmed) {
		return true
	}

	return false
}

// isListItemMarker reports whether line (already left-trimmed)
// starts with a CommonMark list-item marker followed by a space
// or tab.
func isListItemMarker(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	if c == '-' || c == '*' || c == '+' {
		if len(line) >= 2 && (line[1] == ' ' || line[1] == '\t') {
			return true
		}
		return false
	}
	// Ordered list: digits followed by '.' or ')' and a space.
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 {
		return false
	}
	if i >= len(line) {
		return false
	}
	if line[i] != '.' && line[i] != ')' {
		return false
	}
	if i+1 >= len(line) {
		return false
	}
	return line[i+1] == ' ' || line[i+1] == '\t'
}

// isSetextUnderlineCandidate reports whether line (with optional
// leading whitespace) consists entirely of '=' or entirely of '-'
// characters with optional trailing whitespace. CommonMark
// requires no leading whitespace on the underline; we accept up
// to three spaces for safety so an indented underline still
// blocks a split.
func isSetextUnderlineCandidate(line string) bool {
	// Strip leading whitespace.
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return false
	}
	c := line[i]
	if c != '=' && c != '-' {
		return false
	}
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	// Allow trailing whitespace.
	for j < len(line) {
		if line[j] != ' ' && line[j] != '\t' {
			return false
		}
		j++
	}
	// Need at least one underline character. "-" alone is also a
	// list marker without a trailing space; the listItem check
	// covers the marker case before we get here.
	return j-i >= 1
}

// isHTMLBlockOpener reports whether line begins one of the seven
// CommonMark HTML block patterns. We accept up to three spaces of
// leading indentation (CommonMark rule). Matching is intentionally
// loose — we only need to know the line "looks like an HTML
// block start", not parse the contained markup.
func isHTMLBlockOpener(line string) bool {
	// Strip up to 3 spaces of indentation.
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	rest := line[i:]
	if len(rest) < 2 || rest[0] != '<' {
		return false
	}

	// Type 2: HTML comment "<!--".
	if strings.HasPrefix(rest, "<!--") {
		return true
	}
	// Type 3: processing instruction "<?".
	if strings.HasPrefix(rest, "<?") {
		return true
	}
	// Type 5: CDATA "<![CDATA[".
	if strings.HasPrefix(rest, "<![CDATA[") {
		return true
	}
	// Type 4: declaration "<!" followed by an ASCII letter.
	if len(rest) >= 3 && rest[1] == '!' && isASCIILetter(rest[2]) {
		return true
	}

	// Type 1: <script | <pre | <style | <textarea (case-insensitive)
	// followed by whitespace, '>', end-of-line, or other non-name
	// terminators. Use a permissive HasPrefix check on lowercase.
	low := strings.ToLower(rest)
	for _, t := range []string{"<script", "<pre", "<style", "<textarea"} {
		if strings.HasPrefix(low, t) {
			next := byte(0)
			if len(low) > len(t) {
				next = low[len(t)]
			}
			if next == 0 || next == ' ' || next == '\t' || next == '>' {
				return true
			}
		}
	}

	// Types 6 & 7: open or close of a block-level tag.
	//
	// Type 6 matches a fixed CommonMark tag set; type 7 matches any
	// otherwise-valid open/close tag whose name is not in the
	// script/pre/style/textarea family. We collapse both into a
	// single check: the line must start with '<' or '</' followed
	// by an ASCII letter. This deliberately mirrors the other
	// hazards — when in doubt, forfeit the boundary. Lines like
	// "<3", "<-", "<<", or mid-line "<foo>" do NOT trigger because
	// we require the line to *start* (after up to 3 spaces) with
	// '<letter' or '</letter'.
	j := 1 // past '<'
	if j < len(rest) && rest[j] == '/' {
		j++
	}
	if j >= len(rest) || !isASCIILetter(rest[j]) {
		return false
	}
	return true
}

// isASCIILetter reports whether b is an ASCII letter.
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isLinkRefDefinition reports whether line matches a CommonMark
// link reference definition opener. The conservative pattern:
//
//	^[ ]{0,3}\[[^\]]+\]:\s*\S+
//
// i.e. up to 3 spaces, then a bracketed label (no nested ']'),
// then a colon, then whitespace, then at least one non-whitespace
// character of destination. We do not validate the destination —
// presence of a ref-def opener anywhere in the prefix is enough
// to forfeit the boundary.
func isLinkRefDefinition(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || line[i] != '[' {
		return false
	}
	i++
	labelStart := i
	for i < len(line) && line[i] != ']' {
		i++
	}
	if i >= len(line) || i == labelStart {
		// No closing bracket, or empty label.
		return false
	}
	// i points at ']'.
	i++
	if i >= len(line) || line[i] != ':' {
		return false
	}
	i++
	// Skip required whitespace.
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	// At least one non-whitespace character of destination.
	return i < len(line)
}

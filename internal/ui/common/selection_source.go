package common

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// alignNearWindow is how far ahead in the source the aligner looks for
	// a rendered rune before falling back to a full forward scan. It covers
	// typical markdown syntax runs (fences, table separators, link URLs).
	alignNearWindow = 48
	// alignNearConfirm is the number of following word runes that must
	// agree for a near match to be accepted.
	alignNearConfirm = 3
	// alignFarConfirm is the stricter confirmation used for far matches,
	// which typically occur after truncated or collapsed rendered output.
	alignFarConfirm = 8
	// anchorLen is the length in non-space runes of the substrings used as
	// alignment anchors. It is long enough that a substring occurring once
	// in each text almost always marks the same content in both.
	anchorLen = 10
)

// inlineMarkers are markdown inline syntax characters that glamour strips
// from the rendered output and that should travel with a copied selection.
const inlineMarkers = "`*_~"

// blockPrefixRe matches source text between a line start and the first
// content character that consists only of block-level markdown markers
// (headings, quotes, list bullets, task boxes, ordered list numbers).
var blockPrefixRe = regexp.MustCompile(`^[ \t]*(?:(?:#{1,6}|>|[-*+](?:[ \t]+\[[ xX]\])?|\d+[.)])[ \t]*)+$`)

// fenceRe matches the opening or closing line of a fenced code block.
var fenceRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

// hiddenSourceRes match markdown source text that glamour never renders in
// place: link and image titles (the first submatch), reference definition
// lines, and HTML comments.
var hiddenSourceRes = []*regexp.Regexp{
	regexp.MustCompile(`\]\(\s*(?:<[^>]*>|[^\s()]+)\s+("[^"]*"|'[^']*'|\([^)]*\))\s*\)`),
	regexp.MustCompile(`(?m)^ {0,3}\[[^\]]+\]:[ \t]*\S+.*$`),
	regexp.MustCompile(`(?s)<!--.*?-->`),
}

// MapSelectionToSource maps a selection over glamour-rendered plain text
// back to the markdown source it was rendered from. rendered is the plain
// (ANSI-free) rendered text and [start, end) is the selected byte range
// within it. It returns the corresponding markdown, including the inline
// and block syntax that rendering removed. ok is false when the selection
// cannot be mapped (for example when it only covers decorations that have
// no counterpart in the source).
func MapSelectionToSource(source, rendered string, start, end int) (string, bool) {
	if source == "" || start < 0 || end <= start {
		return "", false
	}
	start = min(start, len(rendered))
	end = min(end, len(rendered))

	rPos, rRunes := nonSpaceRunes(rendered)
	sPos, sRunes := nonSpaceRunes(source)
	if len(rRunes) == 0 || len(sRunes) == 0 {
		return "", false
	}
	mapping := alignRunes(rRunes, sRunes, hiddenRunes(source, sPos))
	scan := scanSelection(rendered, rPos, rRunes, mapping, start, end)
	if !scan.mappable() {
		return "", false
	}
	firstSel, lastSel := scan.firstSel, scan.lastSel

	srcStart := sPos[mapping[firstSel]]
	lastRune := sRunes[mapping[lastSel]]
	srcEnd := sPos[mapping[lastSel]] + utf8.RuneLen(lastRune)

	if firstSel == scan.firstAll {
		srcStart = 0
	} else {
		if !scan.mappedBeforeOnLine {
			ls := strings.LastIndexByte(source[:srcStart], '\n') + 1
			if blockPrefixRe.MatchString(source[ls:srcStart]) {
				srcStart = ls
			}
		}
		for srcStart > 0 && strings.IndexByte(inlineMarkers, source[srcStart-1]) >= 0 {
			srcStart--
		}
	}
	if lastSel == scan.lastAll {
		srcEnd = len(source)
	} else {
		for srcEnd < len(source) && strings.IndexByte(inlineMarkers, source[srcEnd]) >= 0 {
			srcEnd++
		}
	}

	srcStart, srcEnd, prefix, suffix := balanceFences(source, srcStart, srcEnd)

	text := strings.TrimRight(source[srcStart:srcEnd], " \t\r\n")
	text = strings.TrimLeft(text, "\r\n")
	if text == "" {
		return "", false
	}
	return prefix + text + suffix, true
}

// selectionScan summarises how a selection over the rendered text lines up
// with the source. Rune fields are indexes into the rendered non-space
// runes and are -1 when there is no such rune.
type selectionScan struct {
	// firstAll and lastAll are the first and last mapped runes overall.
	firstAll, lastAll int
	// firstSel and lastSel are the first and last mapped selected runes.
	firstSel, lastSel int
	// mappedBeforeOnLine reports whether a mapped rune precedes the
	// selection on its first line.
	mappedBeforeOnLine bool
	// selectedWords counts selected word runes, and mappedWords those of
	// them with a source counterpart.
	selectedWords, mappedWords int
	// firstWordMapped and lastWordMapped report whether the first and last
	// selected word runes have a source counterpart.
	firstWordMapped, lastWordMapped bool
}

func scanSelection(rendered string, rPos []int, rRunes []rune, mapping []int, start, end int) selectionScan {
	scan := selectionScan{firstAll: -1, lastAll: -1, firstSel: -1, lastSel: -1}
	lineStart := strings.LastIndexByte(rendered[:start], '\n') + 1
	for i, m := range mapping {
		pos := rPos[i]
		selected := pos >= start && pos < end
		if selected && isWordRune(rRunes[i]) {
			if scan.selectedWords == 0 {
				scan.firstWordMapped = m >= 0
			}
			scan.lastWordMapped = m >= 0
			scan.selectedWords++
			if m >= 0 {
				scan.mappedWords++
			}
		}
		if m < 0 {
			continue
		}
		if scan.firstAll < 0 {
			scan.firstAll = i
		}
		scan.lastAll = i
		if pos >= lineStart && pos < start {
			scan.mappedBeforeOnLine = true
		}
		if !selected {
			continue
		}
		if scan.firstSel < 0 {
			scan.firstSel = i
		}
		scan.lastSel = i
	}
	return scan
}

// mappable reports whether the selection can be trusted to map to the
// source. Mostly unmapped word runes mean the selection is largely
// rendered-only text, or that alignment went wrong, so the few runes that
// matched would not be what the user selected. An unmapped first or last
// word rune would cut the mapped range short of the selection.
func (s selectionScan) mappable() bool {
	if s.firstSel < 0 || s.mappedWords*2 < s.selectedWords {
		return false
	}
	return s.selectedWords == 0 || s.firstWordMapped && s.lastWordMapped
}

// nonSpaceRunes returns the non-whitespace runes of s with their byte
// offsets.
func nonSpaceRunes(s string) (positions []int, runes []rune) {
	for i, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		positions = append(positions, i)
		runes = append(runes, r)
	}
	return positions, runes
}

// alignRunes aligns rendered runes to source runes, returning for each
// rendered rune the index of its source rune or -1 when the rune is a
// rendering decoration (bullets, borders, labels, link references) with no
// source counterpart. Matches are monotonic. sourceHidden marks source
// runes that are never rendered in place; they can't be anchors.
//
// Runs of word runes that occur exactly once in each text are paired up as
// anchors, and the longest chain of anchors in the same order in both
// texts is kept. The runes between consecutive anchors are aligned
// greedily, so a wrong greedy match can't skip past the next anchor.
func alignRunes(rendered, source []rune, sourceHidden []bool) []int {
	mapping := make([]int, len(rendered))
	ri, si := 0, 0
	for _, a := range anchorChain(rendered, source, sourceHidden) {
		alignGap(mapping, rendered, source, ri, a.rendered, si, a.source)
		mapping[a.rendered] = a.source
		ri, si = a.rendered+1, a.source+1
	}
	alignGap(mapping, rendered, source, ri, len(rendered), si, len(source))
	return mapping
}

// anchor pairs a rendered rune index with the source rune index of the
// same content.
type anchor struct {
	rendered, source int
}

// anchorChain returns the longest chain of anchors whose rendered and
// source indexes both strictly increase. Anchors are the starts of runs of
// anchorLen word runes that occur exactly once in each text, ignoring
// hidden source runes. Only word runes count, because markdown syntax
// splits visible source text (**bold**) while hidden text such as a link
// title doesn't, which would otherwise make the hidden copy look like the
// unique match.
func anchorChain(rendered, source []rune, sourceHidden []bool) []anchor {
	rWords, rAt := wordRunes(rendered, nil)
	sWords, sAt := wordRunes(source, sourceHidden)
	sourceAt := uniqueSubstrings(sWords)
	renderedAt := uniqueSubstrings(rWords)
	var candidates []anchor
	for i := 0; i+anchorLen <= len(rWords); i++ {
		key := string(rWords[i : i+anchorLen])
		if renderedAt[key] != i {
			continue
		}
		if j, ok := sourceAt[key]; ok && j >= 0 {
			candidates = append(candidates, anchor{rendered: rAt[i], source: sAt[j]})
		}
	}
	return longestIncreasing(candidates)
}

// wordRunes returns the word runes of runes that aren't marked in skip,
// with their indexes. A nil skip keeps every word rune.
func wordRunes(runes []rune, skip []bool) (words []rune, at []int) {
	for i, r := range runes {
		if isWordRune(r) && (skip == nil || !skip[i]) {
			words = append(words, r)
			at = append(at, i)
		}
	}
	return words, at
}

// hiddenRunes reports for each non-space source rune, at the byte offsets
// in pos, whether it lies in text matched by hiddenSourceRes.
func hiddenRunes(source string, pos []int) []bool {
	hiddenByte := make([]bool, len(source))
	for _, re := range hiddenSourceRes {
		for _, m := range re.FindAllStringSubmatchIndex(source, -1) {
			from, to := m[0], m[1]
			if len(m) > 2 {
				from, to = m[2], m[3]
			}
			for b := from; b < to; b++ {
				hiddenByte[b] = true
			}
		}
	}
	hidden := make([]bool, len(pos))
	for i, p := range pos {
		hidden[i] = hiddenByte[p]
	}
	return hidden
}

// uniqueSubstrings maps each anchorLen-rune substring of runes to the index
// where it starts, or to -1 when it occurs more than once.
func uniqueSubstrings(runes []rune) map[string]int {
	at := make(map[string]int, max(len(runes)-anchorLen+1, 0))
	for i := 0; i+anchorLen <= len(runes); i++ {
		key := string(runes[i : i+anchorLen])
		if _, seen := at[key]; seen {
			at[key] = -1
			continue
		}
		at[key] = i
	}
	return at
}

// longestIncreasing returns the longest subsequence of anchors, which are
// ordered by rendered index, whose source indexes strictly increase.
func longestIncreasing(anchors []anchor) []anchor {
	// tails[n] is the index in anchors of the smallest source index that
	// ends an increasing chain of length n+1.
	var tails []int
	prev := make([]int, len(anchors))
	for i, a := range anchors {
		n := sort.Search(len(tails), func(n int) bool {
			return anchors[tails[n]].source >= a.source
		})
		prev[i] = -1
		if n > 0 {
			prev[i] = tails[n-1]
		}
		if n == len(tails) {
			tails = append(tails, i)
		} else {
			tails[n] = i
		}
	}
	chain := make([]anchor, len(tails))
	if len(tails) == 0 {
		return chain
	}
	for n, i := len(tails)-1, tails[len(tails)-1]; n >= 0; n, i = n-1, prev[i] {
		chain[n] = anchors[i]
	}
	return chain
}

// alignGap greedily aligns the rendered runes in [rFrom, rTo) to the source
// runes in [sFrom, sTo), writing into mapping. Matches are confirmed by the
// word runes that follow, read past the end of the gap, so source-only
// syntax and rendered-only decorations are skipped.
func alignGap(mapping []int, rendered, source []rune, rFrom, rTo, sFrom, sTo int) {
	j := sFrom
	for i := rFrom; i < rTo; i++ {
		mapping[i] = -1
		if j >= sTo {
			continue
		}
		k := findAlignedRune(rendered, i, source, j, min(sTo, j+alignNearWindow+1), alignNearConfirm)
		if k < 0 && isWordRune(rendered[i]) {
			k = findAlignedRune(rendered, i, source, j, sTo, alignFarConfirm)
		}
		if k >= 0 {
			mapping[i] = k
			j = k + 1
		}
	}
}

// findAlignedRune finds the first source index in [from, limit) whose rune
// equals rendered[i] and whose following word runes agree with the
// rendered text. It returns -1 when no such index exists.
func findAlignedRune(rendered []rune, i int, source []rune, from, limit, confirm int) int {
	for k := from; k < limit; k++ {
		if source[k] == rendered[i] && wordRunesAgree(rendered, i+1, source, k+1, confirm) {
			return k
		}
	}
	return -1
}

// wordRunesAgree reports whether the next n word runes of rendered starting
// at i match the next n word runes of source starting at j. Running out of
// rendered runes counts as agreement; running out of source runes does not.
func wordRunesAgree(rendered []rune, i int, source []rune, j, n int) bool {
	for range n {
		for i < len(rendered) && !isWordRune(rendered[i]) {
			i++
		}
		if i >= len(rendered) {
			return true
		}
		for j < len(source) && !isWordRune(source[j]) {
			j++
		}
		if j >= len(source) || rendered[i] != source[j] {
			return false
		}
		i++
		j++
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// fenceBlock describes a fenced code block by the byte offsets of its
// opening and closing fence lines (line ends exclude the newline).
type fenceBlock struct {
	openStart, openEnd   int
	closeStart, closeEnd int
	closed               bool
	marker               string
}

// findFenceBlocks locates fenced code blocks in source.
func findFenceBlocks(source string) []fenceBlock {
	var blocks []fenceBlock
	var cur *fenceBlock
	offset := 0
	for line := range strings.SplitSeq(source, "\n") {
		lineStart, lineEnd := offset, offset+len(line)
		offset = lineEnd + 1
		m := fenceRe.FindStringSubmatch(line)
		if cur == nil {
			if m != nil {
				cur = &fenceBlock{openStart: lineStart, openEnd: lineEnd, marker: m[1]}
			}
			continue
		}
		if m != nil && m[1][0] == cur.marker[0] && len(m[1]) >= len(cur.marker) &&
			strings.TrimSpace(line[len(m[0]):]) == "" {
			cur.closeStart, cur.closeEnd, cur.closed = lineStart, lineEnd, true
			blocks = append(blocks, *cur)
			cur = nil
		}
	}
	if cur != nil {
		cur.closeStart, cur.closeEnd = len(source), len(source)
		blocks = append(blocks, *cur)
	}
	return blocks
}

// balanceFences adjusts [start, end) so that fenced code blocks in the
// selection stay well formed. A selection covering a block's whole content
// grows to include its fences; a selection that crosses only one fence gets
// the missing fence added as a prefix or suffix.
func balanceFences(source string, start, end int) (newStart, newEnd int, prefix, suffix string) {
	for _, b := range findFenceBlocks(source) {
		if end <= b.openStart || start >= b.closeEnd {
			continue
		}
		contentStart := min(b.openEnd+1, len(source))
		contentEnd := max(b.closeStart, contentStart)
		openIn := start <= b.openStart && end >= b.openEnd
		closeIn := b.closed && start <= b.closeStart && end >= b.closeEnd
		beforeEmpty := strings.TrimSpace(source[contentStart:max(start, contentStart)]) == ""
		afterEmpty := strings.TrimSpace(source[min(end, contentEnd):contentEnd]) == ""
		switch {
		case openIn && closeIn:
		case !openIn && !closeIn:
			if beforeEmpty && afterEmpty && b.closed {
				start, end = b.openStart, b.closeEnd
			}
		case openIn:
			if afterEmpty && b.closed {
				end = b.closeEnd
			} else {
				suffix = "\n" + b.marker
			}
		default:
			if beforeEmpty {
				start = b.openStart
			} else {
				start = strings.LastIndexByte(source[:start], '\n') + 1
				prefix = source[b.openStart:b.openEnd] + "\n"
			}
		}
	}
	return start, end, prefix, suffix
}

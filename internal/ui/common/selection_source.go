package common

import (
	"regexp"
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
	mapping := alignRunes(rRunes, sRunes)

	firstAll, lastAll := -1, -1
	firstSel, lastSel := -1, -1
	mappedBeforeOnLine := false
	lineStart := strings.LastIndexByte(rendered[:start], '\n') + 1
	for i, m := range mapping {
		if m < 0 {
			continue
		}
		if firstAll < 0 {
			firstAll = i
		}
		lastAll = i
		pos := rPos[i]
		if pos >= lineStart && pos < start {
			mappedBeforeOnLine = true
		}
		if pos < start || pos >= end {
			continue
		}
		if firstSel < 0 {
			firstSel = i
		}
		lastSel = i
	}
	if firstSel < 0 {
		return "", false
	}

	srcStart := sPos[mapping[firstSel]]
	lastRune := sRunes[mapping[lastSel]]
	srcEnd := sPos[mapping[lastSel]] + utf8.RuneLen(lastRune)

	if firstSel == firstAll {
		srcStart = 0
	} else {
		if !mappedBeforeOnLine {
			ls := strings.LastIndexByte(source[:srcStart], '\n') + 1
			if blockPrefixRe.MatchString(source[ls:srcStart]) {
				srcStart = ls
			}
		}
		for srcStart > 0 && strings.IndexByte(inlineMarkers, source[srcStart-1]) >= 0 {
			srcStart--
		}
	}
	if lastSel == lastAll {
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

// alignRunes greedily aligns rendered runes to source runes, returning for
// each rendered rune the index of its source rune or -1 when the rune is a
// rendering decoration (bullets, borders, labels) with no source
// counterpart. Matches are monotonic and confirmed by the word runes that
// follow, so source-only syntax and rendered-only decorations are skipped.
func alignRunes(rendered, source []rune) []int {
	mapping := make([]int, len(rendered))
	j := 0
	for i := range rendered {
		mapping[i] = -1
		if j >= len(source) {
			continue
		}
		k := findAlignedRune(rendered, i, source, j, alignNearWindow, alignNearConfirm)
		if k < 0 && isWordRune(rendered[i]) {
			k = findAlignedRune(rendered, i, source, j, len(source), alignFarConfirm)
		}
		if k >= 0 {
			mapping[i] = k
			j = k + 1
		}
	}
	return mapping
}

// findAlignedRune finds the first source index in [from, from+window] whose
// rune equals rendered[i] and whose following word runes agree with the
// rendered text. It returns -1 when no such index exists.
func findAlignedRune(rendered []rune, i int, source []rune, from, window, confirm int) int {
	limit := min(len(source), from+window+1)
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

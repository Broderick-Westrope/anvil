package mermaid

import "strings"

// Segment is a run of markdown: text, or the source of a top-level mermaid
// fence without its fence lines.
type Segment struct {
	Text    string
	Mermaid bool
	// Line is the 1-based line of the content Text starts on. A diagram
	// starts on the line after its opening fence.
	Line int
}

// codeFence is an open fenced code block: its fence character and length.
type codeFence struct {
	char byte
	n    int
}

// openCodeFence reports whether line opens a fenced code block at the top
// level, and returns the first word of its info string.
func openCodeFence(line string) (codeFence, string, bool) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return codeFence{}, "", false
	}
	char := line[0]
	n := len(line) - len(strings.TrimLeft(line, string(char)))
	info := strings.TrimSpace(line[n:])
	if n < 3 || (char == '`' && strings.Contains(info, "`")) {
		return codeFence{}, "", false
	}
	lang, _, _ := strings.Cut(info, " ")
	return codeFence{char: char, n: n}, lang, true
}

// closedBy reports whether line closes the fence: the same character, at
// least as many times, and nothing else.
func (f codeFence) closedBy(line string) bool {
	line = strings.TrimRight(line, " \t\r\n")
	return len(line) >= f.n && strings.Trim(line, string(f.char)) == ""
}

// Split cuts content at every closed mermaid fence that starts a line at
// the top level. Fences indented into a list item or quoted in a blockquote
// stay in the text, since cutting there would break the list or quote in
// two, and so do mermaid examples inside another fence. These are the
// fences the chat draws as diagrams.
func Split(content string) []Segment {
	var segs []Segment
	var text, diagram strings.Builder
	var fence codeFence
	var opening string
	textLine, diagramLine := 1, 0
	inFence, inMermaid := false, false
	writeText := func(s string, line int) {
		if text.Len() == 0 {
			textLine = line
		}
		text.WriteString(s)
	}
	line := 0
	for l := range strings.SplitAfterSeq(content, "\n") {
		line++
		switch {
		case inMermaid && fence.closedBy(l):
			if text.Len() > 0 {
				segs = append(segs, Segment{Text: text.String(), Line: textLine})
				text.Reset()
			}
			segs = append(segs, Segment{Text: diagram.String(), Mermaid: true, Line: diagramLine})
			diagram.Reset()
			inFence, inMermaid = false, false
		case inMermaid:
			diagram.WriteString(l)
		case inFence:
			writeText(l, line)
			inFence = !fence.closedBy(l)
		default:
			f, lang, ok := openCodeFence(l)
			fence, inFence = f, ok
			if ok && lang == "mermaid" {
				opening, inMermaid, diagramLine = l, true, line+1
			} else {
				writeText(l, line)
			}
		}
	}
	if inMermaid {
		// Unclosed: keep it as the markdown it was.
		writeText(opening+diagram.String(), diagramLine-1)
	}
	if text.Len() > 0 {
		segs = append(segs, Segment{Text: text.String(), Line: textLine})
	}
	return segs
}

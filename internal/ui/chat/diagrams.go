package chat

import (
	"strings"

	"charm.land/glamour/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
)

// DiagramRenderer turns Mermaid source into terminal output that stands in
// for its code block.
type DiagramRenderer interface {
	// Diagram returns the rendered diagram for source, at most width
	// columns wide, or false when it isn't ready (the render is then
	// queued) or can't be shown, in which case the code block is kept.
	Diagram(source string, width int) (string, bool)
}

// Diagrams renders Mermaid fences in finished assistant messages. Nil
// leaves them as code blocks.
//
// SPIKE: a package-level hook keeps the experiment out of the item
// constructors. A real implementation would thread it through
// NewAssistantMessageItem.
var Diagrams DiagramRenderer

type markdownSegment struct {
	text    string
	mermaid bool
}

// splitMermaid cuts content at every closed ```mermaid fence. Mermaid
// segments hold the diagram source without the fence lines.
func splitMermaid(content string) []markdownSegment {
	var segs []markdownSegment
	var text, diagram strings.Builder
	inMermaid := false
	inOtherFence := false
	for line := range strings.SplitAfterSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inMermaid:
			if strings.HasPrefix(trimmed, "```") && strings.Trim(trimmed, "`") == "" {
				if text.Len() > 0 {
					segs = append(segs, markdownSegment{text: text.String()})
					text.Reset()
				}
				segs = append(segs, markdownSegment{text: diagram.String(), mermaid: true})
				diagram.Reset()
				inMermaid = false
				continue
			}
			diagram.WriteString(line)
		case inOtherFence:
			text.WriteString(line)
			if strings.HasPrefix(trimmed, "```") && strings.Trim(trimmed, "`") == "" {
				inOtherFence = false
			}
		case strings.HasPrefix(trimmed, "```mermaid"):
			inMermaid = true
		case strings.HasPrefix(trimmed, "```"):
			inOtherFence = true
			text.WriteString(line)
		default:
			text.WriteString(line)
		}
	}
	if inMermaid {
		// Unclosed: keep it as the markdown it was.
		text.WriteString("```mermaid\n" + diagram.String())
	}
	if text.Len() > 0 {
		segs = append(segs, markdownSegment{text: text.String()})
	}
	return segs
}

// renderWithDiagrams renders content as markdown with each Mermaid fence
// replaced by its diagram, when one is ready. It returns false when content
// has no Mermaid fence.
func renderWithDiagrams(content string, width int, renderer *glamour.TermRenderer) (string, bool) {
	if Diagrams == nil || !strings.Contains(content, "```mermaid") {
		return "", false
	}
	segs := splitMermaid(content)
	diagrams := make([]string, len(segs))
	anyReady := false
	for i, s := range segs {
		if !s.mermaid {
			continue
		}
		if out, ok := Diagrams.Diagram(s.text, width); ok {
			diagrams[i] = out
			anyReady = true
		}
	}
	if !anyReady {
		// Keep the monolithic render until there's something to show.
		return "", false
	}

	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	defer mu.Unlock()

	var parts []string
	for i, s := range segs {
		if diagrams[i] != "" {
			parts = append(parts, diagrams[i])
			continue
		}
		if s.mermaid {
			s.text = "```mermaid\n" + s.text + "```\n"
		}
		out, err := renderer.Render(s.text)
		if err != nil {
			out = s.text
		}
		if out = trimGlamourMargins(out); out != "" {
			parts = append(parts, out)
		}
	}
	return strings.Join(parts, "\n\n"), true
}

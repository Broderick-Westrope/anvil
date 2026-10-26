package mermaid

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	pele "github.com/Broderick-Westrope/pele-go"
	"github.com/Broderick-Westrope/pele-go/raster"
)

// Problem is a diagram that doesn't render.
type Problem struct {
	// Diagram is the diagram's 1-based position among those checked.
	Diagram int
	// Line is the 1-based line of the checked content the failure is on,
	// or 0 when the renderer gave no position.
	Line int
	// Message is the renderer's message, which for a parse error holds the
	// offending line with a caret under it.
	Message string
	// Hint suggests a fix for a failure models often make, or is "".
	Hint string
}

// Report is the result of checking content for diagrams that don't render.
type Report struct {
	Diagrams int
	Problems []Problem
}

// CheckMarkdown renders every diagram the chat would draw from content: the
// top-level mermaid fences that Split finds. It returns an error only when
// the renderer can't load.
func CheckMarkdown(ctx context.Context, content string) (Report, error) {
	var diagrams []Segment
	for _, s := range Split(content) {
		if s.Mermaid {
			diagrams = append(diagrams, s)
		}
	}
	return check(ctx, diagrams)
}

// CheckDiagram renders source, the body of one diagram without fence lines.
func CheckDiagram(ctx context.Context, source string) (Report, error) {
	return check(ctx, []Segment{{Text: source, Mermaid: true, Line: 1}})
}

// check renders with the renderer the chat uses, so a diagram passes
// exactly when the chat can draw it. A renderer is cheap to make (under a
// millisecond and about 50 KB), so none is kept between calls.
func check(ctx context.Context, diagrams []Segment) (Report, error) {
	report := Report{Diagrams: len(diagrams)}
	if len(diagrams) == 0 {
		return report, nil
	}
	renderer, err := raster.NewRenderer()
	if err != nil {
		return Report{}, fmt.Errorf("load diagram renderer: %w", err)
	}
	for i, d := range diagrams {
		_, err := renderer.Render(ctx, d.Text, raster.Options{})
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		report.Problems = append(report.Problems, problemFor(i+1, d, err))
	}
	return report, nil
}

func problemFor(diagram int, d Segment, err error) Problem {
	p := Problem{Diagram: diagram, Message: err.Error()}
	var pe *pele.Error
	if errors.As(err, &pe) {
		p.Message = pe.Message
		if pe.Line > 0 {
			// An error at the end of input can be reported on a line past
			// the last one, which in a file would point outside the fence.
			lines := max(strings.Count(strings.TrimSuffix(d.Text, "\n"), "\n")+1, 1)
			p.Line = d.Line + min(pe.Line, lines) - 1
		}
	}
	p.Hint = hintFor(p.Message)
	return p
}

// keywordAsActor matches a sequence diagram parse error where a keyword
// such as "end", "off" or "opt" stands where a participant ID should be.
// The lexer ignores case, so an ID like "Off" or "End" hits it too.
var keywordAsActor = regexp.MustCompile(`Expecting [^\n]*'ACTOR'[^\n]*, got '([a-z_]+)'`)

func hintFor(message string) string {
	m := keywordAsActor.FindStringSubmatch(message)
	if m == nil {
		return ""
	}
	word := strings.ReplaceAll(m[1], "_", " ")
	return fmt.Sprintf("%q is a sequence diagram keyword (matched in any case), so it can't be a participant ID. "+
		"Rename the ID everywhere it's used, and keep the shown name with `participant <ID> as <name>`.", word)
}

// String describes the report for a model: whether every diagram renders,
// and for each one that doesn't, where and why.
func (r Report) String() string {
	if r.Diagrams == 0 {
		return "No mermaid diagrams found. Only fences that start at the beginning of a line, outside lists, quotes and other fences, are drawn as diagrams."
	}
	if len(r.Problems) == 0 {
		if r.Diagrams == 1 {
			return "The diagram renders."
		}
		return fmt.Sprintf("All %d diagrams render.", r.Diagrams)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d diagrams don't render:", len(r.Problems), r.Diagrams)
	for _, p := range r.Problems {
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "Diagram %d", p.Diagram)
		if p.Line > 0 {
			fmt.Fprintf(&b, ", line %d", p.Line)
		}
		b.WriteString(":\n")
		b.WriteString(p.Message)
		if p.Hint != "" {
			b.WriteString("\nHint: ")
			b.WriteString(p.Hint)
		}
	}
	return b.String()
}

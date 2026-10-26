package mermaid

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	// Inconclusive is set when the check couldn't tell whether the diagram
	// is valid: it timed out, was too large, or crashed the renderer.
	Inconclusive bool
}

// Report is the result of checking content for diagrams that don't render.
type Report struct {
	Diagrams int
	Problems []Problem
}

// Conclusive returns the report without the problems the check couldn't
// be sure of.
func (r Report) Conclusive() Report {
	r.Problems = slices.DeleteFunc(slices.Clone(r.Problems), func(p Problem) bool { return p.Inconclusive })
	return r
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
		err := render(ctx, renderer, d.Text)
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

func render(ctx context.Context, renderer *raster.Renderer, source string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", errRendererCrashed, r)
		}
	}()
	_, err = renderer.Render(ctx, source, raster.Options{})
	return err
}

var errRendererCrashed = errors.New("diagram renderer crashed")

func problemFor(diagram int, d Segment, err error) Problem {
	p := Problem{Diagram: diagram, Message: err.Error()}
	var pe *pele.Error
	switch {
	case errors.Is(err, raster.ErrRenderTimeout), errors.Is(err, raster.ErrSourceTooLarge), errors.Is(err, errRendererCrashed):
		p.Inconclusive = true
	case errors.As(err, &pe):
		p.Message = pe.Message
		p.Inconclusive = pe.Code == pele.CodeInternal
		if pe.Line > 0 {
			lines := max(strings.Count(strings.TrimSuffix(d.Text, "\n"), "\n")+1, 1)
			p.Line = d.Line + min(pe.Line, lines) - 1
		}
	}
	p.Hint = hintFor(p.Message)
	return p
}

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
	if r.Diagrams == 1 {
		b.WriteString("The diagram doesn't render:")
	} else {
		fmt.Fprintf(&b, "%d of %d diagrams don't render:", len(r.Problems), r.Diagrams)
	}
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

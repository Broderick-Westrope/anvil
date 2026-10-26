package mermaid_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/stretchr/testify/require"
)

const keywordIDDiagram = `sequenceDiagram
    participant P as Patient
    participant Off as offerings
    P->>Off: buys prescription A
`

func TestCheckMarkdown(t *testing.T) {
	t.Parallel()

	content := "Intro\n\n```mermaid\nflowchart LR\n  A --> B\n```\n\nThen:\n\n```mermaid\n" + keywordIDDiagram + "```\n"
	report, err := mermaid.CheckMarkdown(t.Context(), content)
	require.NoError(t, err)
	require.Equal(t, 2, report.Diagrams)
	require.Len(t, report.Problems, 1)

	p := report.Problems[0]
	require.Equal(t, 2, p.Diagram)
	require.Equal(t, 14, p.Line, "the content line of P->>Off")
	require.Contains(t, p.Message, "got 'off'")
	require.Contains(t, p.Hint, `"off" is a sequence diagram keyword`)

	text := report.String()
	require.Contains(t, text, "1 of 2 diagrams don't render")
	require.Contains(t, text, "Diagram 2, line 14:")
	require.Contains(t, text, "Hint: ")
}

func TestCheckDiagram(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		source   string
		wantLine int
		wantMsg  string
		wantHint bool
		wantText string

		wantInconclusive bool
	}{
		"renders": {
			source:   "sequenceDiagram\n  A->>B: hi\n",
			wantText: "The diagram renders.",
		},
		"keyword as a participant ID": {
			source:   keywordIDDiagram,
			wantLine: 4,
			wantMsg:  "Expecting 'ACTOR', got 'off'",
			wantHint: true,
		},
		"an error at the end of input stays on the last line": {
			source:   "flowchart TD\n  A --> B(\n",
			wantLine: 2,
			wantMsg:  "Parse error",
		},
		"lines count front matter and comments": {
			source:   "---\ntitle: Buying\n---\n%% one\n%% two\nsequenceDiagram\n    participant Off as offerings\n    A->>Off: buys\n",
			wantLine: 8,
			wantMsg:  "got 'off'",
			wantHint: true,
		},
		"too large is inconclusive": {
			source:           "flowchart LR\n" + strings.Repeat("  A --> B\n", 8000),
			wantMsg:          "too large",
			wantInconclusive: true,
		},
		"no diagram type": {
			source:  "just some text",
			wantMsg: "No diagram type detected",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			report, err := mermaid.CheckDiagram(t.Context(), tc.source)
			require.NoError(t, err)
			require.Equal(t, 1, report.Diagrams)
			if tc.wantText != "" {
				require.Empty(t, report.Problems)
				require.Equal(t, tc.wantText, report.String())
				return
			}
			require.Len(t, report.Problems, 1)
			p := report.Problems[0]
			require.Equal(t, tc.wantLine, p.Line)
			require.Contains(t, p.Message, tc.wantMsg)
			require.Equal(t, tc.wantHint, p.Hint != "")
			require.Equal(t, tc.wantInconclusive, p.Inconclusive)
			require.Equal(t, !tc.wantInconclusive, len(report.Conclusive().Problems) == 1)
		})
	}
}

func TestReportString(t *testing.T) {
	t.Parallel()

	require.Contains(t, mermaid.Report{}.String(), "No mermaid diagrams found")
	require.Equal(t, "All 3 diagrams render.", mermaid.Report{Diagrams: 3}.String())
}

func TestCheckMarkdownMarksDiagramsLeftAtTheDeadlineInconclusive(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := mermaid.CheckMarkdown(ctx, "```mermaid\nflowchart LR\n  A --> B\n```\n")
	require.NoError(t, err)
	require.Len(t, report.Problems, 1)
	require.True(t, report.Problems[0].Inconclusive)
	require.Empty(t, report.Conclusive().Problems)
}

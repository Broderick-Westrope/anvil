package mermaid_test

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		content string
		want    []mermaid.Segment
	}{
		"only a diagram makes no empty text": {
			content: "```mermaid\nA\n```\n",
			want:    []mermaid.Segment{{Text: "A\n", Mermaid: true, Line: 2}},
		},
		"lines count from the content's first line": {
			content: "Intro\n\n```mermaid\nA\nB\n```\nOutro\n",
			want: []mermaid.Segment{
				{Text: "Intro\n\n", Line: 1},
				{Text: "A\nB\n", Mermaid: true, Line: 4},
				{Text: "Outro\n", Line: 7},
			},
		},
		"a fence inside another fence stays text": {
			content: "````md\n```mermaid\nA\n```\n````\n",
			want:    []mermaid.Segment{{Text: "````md\n```mermaid\nA\n```\n````\n", Line: 1}},
		},
		"an unclosed fence stays text": {
			content: "Intro\n```mermaid\nA\n",
			want:    []mermaid.Segment{{Text: "Intro\n```mermaid\nA\n", Line: 1}},
		},
		"an unclosed fence alone starts at its fence": {
			content: "```mermaid\nA\n",
			want:    []mermaid.Segment{{Text: "```mermaid\nA\n", Line: 1}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, mermaid.Split(tc.content))
		})
	}
}

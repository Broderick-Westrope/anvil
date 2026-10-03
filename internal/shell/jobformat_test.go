package shell

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFormatRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m00s"},
		{4*time.Minute + 12*time.Second, "4m12s"},
		{2*time.Hour + 3*time.Minute + 10*time.Second, "2h03m"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, FormatRuntime(tt.d))
		})
	}
}

func TestJobLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		info   JobInfo
		maxLen int
		want   string
	}{
		{"description preferred", JobInfo{Command: "sleep 30", Description: "Wait"}, 80, "Wait"},
		{"command fallback", JobInfo{Command: "sleep 30"}, 80, "sleep 30"},
		{"newlines collapsed", JobInfo{Command: "echo a\n  echo b\n"}, 80, "echo a echo b"},
		{"truncated", JobInfo{Command: "abcdefghij"}, 5, "abcd…"},
		{"exact length", JobInfo{Command: "abcde"}, 5, "abcde"},
		{"runes", JobInfo{Command: "ééééé"}, 3, "éé…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := JobLabel(tt.info, tt.maxLen)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len([]rune(got)), tt.maxLen)
		})
	}
}

func TestJobRuntime(t *testing.T) {
	t.Parallel()

	start := time.Now()
	running := JobInfo{StartedAt: start}
	require.Equal(t, 5*time.Second, JobRuntime(running, start.Add(5*time.Second)))

	done := JobInfo{StartedAt: start, CompletedAt: start.Add(3 * time.Second), Done: true}
	require.Equal(t, 3*time.Second, JobRuntime(done, start.Add(time.Hour)))
}

func TestLastLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"empty", "", 3, ""},
		{"fewer lines", "a\nb\n", 3, "a\nb"},
		{"truncated", "a\nb\nc\nd\n", 2, "c\nd"},
		{"no trailing newline", "a\nb\nc", 1, "c"},
		{"zero", "a\nb", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, LastLines(tt.s, tt.n))
		})
	}
}

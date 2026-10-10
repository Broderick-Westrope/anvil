package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripBashDisplayPrefix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, cmd, dir, want string
	}{
		{"strips cd into the project", "cd /repo && go test ./...", "/repo", "go test ./..."},
		{"keeps cd into another dir", "cd /other && go test ./...", "/repo", "cd /other && go test ./..."},
		{"keeps cd into a sibling with the same prefix", "cd /repo2 && ls", "/repo", "cd /repo2 && ls"},
		{"keeps cd into a subdirectory", "cd /repo/sub && ls", "/repo", "cd /repo/sub && ls"},
		{"keeps a bare cd", "cd /repo", "/repo", "cd /repo"},
		{"keeps a cd that is not at the start", "ls && cd /repo && ls", "/repo", "ls && cd /repo && ls"},
		{"no working dir leaves the command alone", "cd  && ls", "", "cd  && ls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, StripBashDisplayPrefix(tc.cmd, tc.dir))
		})
	}
}

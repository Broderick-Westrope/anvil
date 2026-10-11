package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/stretchr/testify/require"
)

// TestHookApproval_SkipsPermissionPrompt checks that a PreToolUse hook's
// approval, stamped on the context for the call, lets a tool that asks for
// permission run without prompting the user.
func TestHookApproval_SkipsPermissionPrompt(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		// setup returns the tool, its input, and a check that the tool
		// did its work.
		setup func(t *testing.T, perms permission.Service, workingDir string) (fantasy.AgentTool, any, func(t *testing.T))
	}{
		"download": {
			setup: func(t *testing.T, perms permission.Service, workingDir string) (fantasy.AgentTool, any, func(t *testing.T)) {
				server := newFetchServer(t, serveBody("text/plain", "abc"))
				params := DownloadParams{URL: server.URL, FilePath: "out.txt"}
				return NewDownloadTool(perms, workingDir, server.Client()), params, func(t *testing.T) {
					got, err := os.ReadFile(filepath.Join(workingDir, "out.txt"))
					require.NoError(t, err)
					require.Equal(t, "abc", string(got))
				}
			},
		},
		"lsp_rename": {
			setup: func(t *testing.T, perms permission.Service, workingDir string) (fantasy.AgentTool, any, func(t *testing.T)) {
				path := writeFakeLangFile(t, workingDir, "a.fakelang", renameTestSource)
				manager := newFakeLSP(t, workingDir, map[string]fakeLSPReply{"textDocument/rename": {Result: fooToBarEdit(path)}})
				params := RenameParams{Symbol: "Foo", NewName: "Bar"}
				return NewRenameTool(manager, perms, nil, workingDir), params, func(t *testing.T) {
					got, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, "func Bar() {}\nBar()\n", string(got))
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			workingDir := t.TempDir()
			perms := permission.NewPermissionService(workingDir, config.YoloOff, nil, nil)
			prompts := perms.Subscribe(t.Context())
			tool, params, verify := tc.setup(t, perms, workingDir)
			input, err := json.Marshal(params)
			require.NoError(t, err)
			call := fantasy.ToolCall{ID: "hook-approved-call", Name: tool.Info().Name, Input: string(input)}
			ctx := permission.WithHookApproval(sessionCtx(), call.ID)

			type result struct {
				resp fantasy.ToolResponse
				err  error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := tool.Run(ctx, call)
				done <- result{resp, err}
			}()

			select {
			case res := <-done:
				require.NoError(t, res.err)
				require.False(t, res.resp.IsError, res.resp.Content)
				verify(t)
			case event := <-prompts:
				perms.Deny(event.Payload, "")
				<-done
				t.Fatalf("hook-approved %s call prompted for permission", name)
			}
		})
	}
}

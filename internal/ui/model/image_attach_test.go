package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/completions"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

// pngHeader is enough for content sniffing to report image/png.
var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func visionConfig(supportsImages bool) *config.Config {
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("test-provider", config.ProviderConfig{
		ID:     "test-provider",
		Models: []catwalk.Model{{ID: "test-model", SupportsImages: supportsImages}},
	})
	return &config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "test-provider", Model: "test-model"},
		},
		Providers: providers,
		Agents: map[string]config.Agent{
			config.AgentOrchestrator: {Model: string(config.SelectedModelTypeLarge)},
		},
	}
}

// mcpResourceWorkspace serves a fixed MCP resource on top of testWorkspace.
type mcpResourceWorkspace struct {
	*testWorkspace
	contents []workspace.MCPResourceContents
}

func (w *mcpResourceWorkspace) ReadMCPResource(context.Context, string, string) ([]workspace.MCPResourceContents, error) {
	return w.contents, nil
}

func newImageAttachUI(t *testing.T, supportsImages bool) *UI {
	t.Helper()
	u := newTestUI()
	u.com.Workspace = &testWorkspace{cfg: visionConfig(supportsImages)}
	u.dialog = dialog.NewOverlay()
	u.slashAC = autocomplete.New(nil, 10)
	return u
}

func writeTestFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

// runCmd executes cmd and any batched commands it returns, collecting every
// resulting message.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, runCmd(c)...)
		}
		return msgs
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func attachmentMsgs(msgs []tea.Msg) []message.Attachment {
	var out []message.Attachment
	for _, msg := range msgs {
		if att, ok := msg.(message.Attachment); ok {
			out = append(out, att)
		}
	}
	return out
}

func hasNoVisionWarning(msgs []tea.Msg) bool {
	for _, msg := range msgs {
		if info, ok := msg.(util.InfoMsg); ok && info.Type == util.InfoTypeWarn && info.Msg == common.NoVisionWarning {
			return true
		}
	}
	return false
}

func TestPasteImagePaths(t *testing.T) {
	t.Parallel()

	t.Run("non-vision model keeps the paths as text and warns", func(t *testing.T) {
		t.Parallel()

		u := newImageAttachUI(t, false)
		path := writeTestFile(t, "shot.png", pngHeader)

		msgs := runCmd(u.handlePasteMsg(tea.PasteMsg{Content: path}))

		require.True(t, hasNoVisionWarning(msgs))
		require.Empty(t, attachmentMsgs(msgs))
		require.Equal(t, path, u.textarea.Value())
	})

	t.Run("vision model attaches the image", func(t *testing.T) {
		t.Parallel()

		u := newImageAttachUI(t, true)
		path := writeTestFile(t, "shot.png", pngHeader)

		msgs := runCmd(u.handlePasteMsg(tea.PasteMsg{Content: path}))

		require.False(t, hasNoVisionWarning(msgs))
		require.Len(t, attachmentMsgs(msgs), 1)
		require.Empty(t, u.textarea.Value())
	})
}

func TestInsertFileCompletionImageGate(t *testing.T) {
	t.Parallel()

	t.Run("non-vision model warns instead of attaching an image", func(t *testing.T) {
		t.Parallel()

		u := newImageAttachUI(t, false)
		path := writeTestFile(t, "shot.png", pngHeader)

		msgs := runCmd(u.insertFileCompletion(path))

		require.True(t, hasNoVisionWarning(msgs))
		require.Empty(t, attachmentMsgs(msgs))
		require.Contains(t, u.textarea.Value(), path)
	})

	t.Run("non-vision model still attaches text files", func(t *testing.T) {
		t.Parallel()

		u := newImageAttachUI(t, false)
		path := writeTestFile(t, "notes.txt", []byte("hello"))

		msgs := runCmd(u.insertFileCompletion(path))

		require.False(t, hasNoVisionWarning(msgs))
		require.Len(t, attachmentMsgs(msgs), 1)
	})

	t.Run("vision model attaches the image", func(t *testing.T) {
		t.Parallel()

		u := newImageAttachUI(t, true)
		path := writeTestFile(t, "shot.png", pngHeader)

		msgs := runCmd(u.insertFileCompletion(path))

		require.False(t, hasNoVisionWarning(msgs))
		require.Len(t, attachmentMsgs(msgs), 1)
	})
}

func TestInsertMCPResourceCompletionImageGate(t *testing.T) {
	t.Parallel()

	resource := func(supportsImages bool, mimeType string) []tea.Msg {
		u := newTestUI()
		u.com.Workspace = &mcpResourceWorkspace{
			testWorkspace: &testWorkspace{cfg: visionConfig(supportsImages)},
			contents:      []workspace.MCPResourceContents{{URI: "res://x", MIMEType: mimeType, Blob: pngHeader}},
		}
		return runCmd(u.insertMCPResourceCompletion(completions.ResourceCompletionValue{
			MCPName: "srv",
			URI:     "res://x",
		}))
	}

	t.Run("non-vision model warns instead of attaching an image", func(t *testing.T) {
		t.Parallel()

		msgs := resource(false, "image/png")
		require.True(t, hasNoVisionWarning(msgs))
		require.Empty(t, attachmentMsgs(msgs))
	})

	t.Run("non-vision model still attaches text resources", func(t *testing.T) {
		t.Parallel()

		msgs := resource(false, "text/plain")
		require.False(t, hasNoVisionWarning(msgs))
		require.Len(t, attachmentMsgs(msgs), 1)
	})

	t.Run("vision model attaches the image", func(t *testing.T) {
		t.Parallel()

		msgs := resource(true, "image/png")
		require.False(t, hasNoVisionWarning(msgs))
		require.Len(t, attachmentMsgs(msgs), 1)
	})
}

func TestOpenFilesDialogWarnsWithoutVision(t *testing.T) {
	t.Parallel()

	u := newImageAttachUI(t, false)

	msgs := runCmd(u.openFilesDialog())

	require.True(t, hasNoVisionWarning(msgs))
}

func TestPasteImageKeyWarnsWithoutVision(t *testing.T) {
	t.Parallel()

	u := newImageAttachUI(t, false)
	u.keyMap = DefaultKeyMap()
	sty := u.com.Styles.Attachments
	u.attachments = attachments.New(
		attachments.NewRenderer(sty),
		attachments.Keymap{},
	)

	msgs := runCmd(u.handleKeyPressMsg(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}))

	require.True(t, hasNoVisionWarning(msgs))
	require.Empty(t, attachmentMsgs(msgs))
}

func TestAddImageKeyWarnsWithoutVision(t *testing.T) {
	t.Parallel()

	u := newImageAttachUI(t, false)
	u.keyMap = DefaultKeyMap()
	u.attachments = attachments.New(attachments.NewRenderer(u.com.Styles.Attachments), attachments.Keymap{})

	msgs := runCmd(u.handleKeyPressMsg(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}))

	require.True(t, hasNoVisionWarning(msgs))
	require.False(t, u.dialog.ContainsDialog(dialog.FilePickerID))
}

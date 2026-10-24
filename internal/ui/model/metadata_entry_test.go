package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

type metadataEntry struct {
	sessionID string
	params    message.CreateMessageParams
}

// metadataWorkspace records metadata entries and accepts the model
// updates the selection handlers make.
type metadataWorkspace struct {
	*testWorkspace
	entries   []metadataEntry
	failWrite bool
	busy      bool
	current   config.SelectedModel
}

func (w *metadataWorkspace) WriteMetadataEntry(_ context.Context, sessionID string, params message.CreateMessageParams) error {
	w.entries = append(w.entries, metadataEntry{sessionID, params})
	if w.failWrite {
		return errors.New("write failed")
	}
	return nil
}

func (w *metadataWorkspace) AgentIsReady() bool { return w.busy }
func (w *metadataWorkspace) AgentIsBusy() bool  { return w.busy }

func (w *metadataWorkspace) AgentModel() workspace.AgentModel {
	return workspace.AgentModel{ModelCfg: w.current}
}

func (*metadataWorkspace) UpdatePreferredModel(config.Scope, config.SelectedModelType, config.SelectedModel) error {
	return nil
}

func (*metadataWorkspace) UpdateAgentModel(context.Context) error { return nil }

func newMetadataTestUI(sess *session.Session) (*UI, *metadataWorkspace) {
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("anthropic", config.ProviderConfig{ID: "anthropic"})
	current := config.SelectedModel{Provider: "anthropic", Model: "opus", ReasoningEffort: "low"}
	cfg := &config.Config{
		Providers: providers,
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: current,
			config.SelectedModelTypeSmall: {Provider: "anthropic", Model: "haiku"},
		},
		Options: &config.Options{},
	}
	ws := &metadataWorkspace{testWorkspace: &testWorkspace{cfg: cfg}, current: current}
	u := newTestUI()
	u.com.Workspace = ws
	u.dialog = dialog.NewOverlay()
	u.session = sess
	return u, ws
}

// dispatch runs action through handleDialogMsg as if the front dialog
// had produced it.
func dispatch(u *UI, action dialog.Action) {
	u.dialog.OpenDialog(&stubDialog{id: "stub", action: action})
	runCmd(u.handleDialogMsg(tea.KeyPressMsg{}))
}

func TestWriteMetadataEntry(t *testing.T) {
	t.Parallel()
	part := message.LabelContent{Label: "x"}
	tests := map[string]struct {
		session   *session.Session
		failWrite bool
		want      []metadataEntry
	}{
		"no session":    {},
		"no branch tip": {session: &session.Session{ID: "s1"}},
		"at the branch tip": {
			session: &session.Session{ID: "s1", LeafMessageID: "leaf"},
			want: []metadataEntry{{"s1", message.CreateMessageParams{
				MessageType: message.MessageTypeLabel, Parts: []message.ContentPart{part},
			}}},
		},
		"a failed write is only logged": {
			session:   &session.Session{ID: "s1", LeafMessageID: "leaf"},
			failWrite: true,
			want: []metadataEntry{{"s1", message.CreateMessageParams{
				MessageType: message.MessageTypeLabel, Parts: []message.ContentPart{part},
			}}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, ws := newMetadataTestUI(tc.session)
			ws.failWrite = tc.failWrite

			cmd := u.writeMetadataEntry(message.MessageTypeLabel, part)

			if tc.want == nil {
				require.Nil(t, cmd)
				return
			}
			require.NotNil(t, cmd)
			// The entry is written when the command runs, not before.
			require.Empty(t, ws.entries)
			require.Nil(t, cmd())
			require.Equal(t, tc.want, ws.entries)
		})
	}
}

func TestSelectModel_RecordsLargeModelChanges(t *testing.T) {
	t.Parallel()
	changed := config.SelectedModel{Provider: "anthropic", Model: "sonnet"}
	tests := map[string]struct {
		modelType config.SelectedModelType
		model     config.SelectedModel
		busy      bool
		want      bool
	}{
		"large model changed": {modelType: config.SelectedModelTypeLarge, model: changed, want: true},
		"provider changed": {
			modelType: config.SelectedModelTypeLarge,
			model:     config.SelectedModel{Provider: "openrouter", Model: "opus"},
			want:      true,
		},
		"same large model": {
			modelType: config.SelectedModelTypeLarge,
			model:     config.SelectedModel{Provider: "anthropic", Model: "opus"},
		},
		"small model": {modelType: config.SelectedModelTypeSmall, model: changed},
		// The entry queues behind the work in flight rather than racing it.
		"agent busy during OAuth flow": {modelType: config.SelectedModelTypeLarge, model: changed, busy: true, want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, ws := newMetadataTestUI(&session.Session{ID: "s1", LeafMessageID: "leaf"})
			u.com.Workspace.Config().Providers.Set("openrouter", config.ProviderConfig{ID: "openrouter"})
			ws.busy = tc.busy
			// A busy agent only lets a selection through from the OAuth
			// dialog, which must stay dismissable.
			u.dialog.OpenDialog(&stubDialog{id: dialog.OAuthID})

			runCmd(u.handleSelectModel(dialog.ActionSelectModel{Model: tc.model, ModelType: tc.modelType}))

			if !tc.want {
				require.Empty(t, ws.entries)
				return
			}
			require.Equal(t, []metadataEntry{{"s1", message.CreateMessageParams{
				MessageType: message.MessageTypeModelChange,
				Parts:       []message.ContentPart{message.ModelChangeContent{Provider: tc.model.Provider, ModelID: tc.model.Model}},
			}}}, ws.entries)
		})
	}
}

func TestSelectReasoningEffort_RecordsChanges(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		effort string
		want   bool
	}{
		"effort changed": {effort: "high", want: true},
		"same effort":    {effort: "low"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, ws := newMetadataTestUI(&session.Session{ID: "s1", LeafMessageID: "leaf"})

			dispatch(u, dialog.ActionSelectReasoningEffort{Effort: tc.effort})

			if !tc.want {
				require.Empty(t, ws.entries)
				return
			}
			require.Equal(t, []metadataEntry{{"s1", message.CreateMessageParams{
				MessageType: message.MessageTypeThinkingLevelChange,
				Parts:       []message.ContentPart{message.ThinkingLevelChangeContent{ThinkingLevel: tc.effort}},
			}}}, ws.entries)
		})
	}
}

func TestWriteMetadataEntry_LogsOnlyFailedWrites(t *testing.T) {
	logs := captureLogs(t)
	u, ws := newMetadataTestUI(&session.Session{ID: "s1", LeafMessageID: "leaf"})
	part := message.LabelContent{Label: "x"}

	require.Nil(t, u.writeMetadataEntry(message.MessageTypeLabel, part)())
	require.NotContains(t, logs.String(), "Failed to write metadata entry")

	ws.failWrite = true
	require.Nil(t, u.writeMetadataEntry(message.MessageTypeLabel, part)())
	require.Contains(t, logs.String(), "Failed to write metadata entry")
}

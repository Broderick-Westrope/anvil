package config

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestYoloLevel_String(t *testing.T) {
	t.Parallel()

	require.Equal(t, "off", YoloOff.String())
	require.Equal(t, "standard", YoloStandard.String())
	require.Equal(t, "full", YoloFull.String())
	require.Contains(t, YoloLevel(99).String(), "YoloLevel(99)")
}

func TestParseYoloLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		want    YoloLevel
		wantErr bool
	}{
		{input: "", want: YoloOff},
		{input: "true", want: YoloStandard},
		{input: "full", want: YoloFull},
		{input: "false", want: YoloOff},
		{input: "invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := ParseYoloLevel(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestYoloMode_Level(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode    YoloMode
		want    YoloLevel
		wantErr bool
	}{
		{mode: "", want: YoloOff},
		{mode: YoloModeOff, want: YoloOff},
		{mode: YoloModeStandard, want: YoloStandard},
		{mode: YoloModeFull, want: YoloFull},
		{mode: "true", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			t.Parallel()
			got, err := tt.mode.Level()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestYolo_GlobalDefaultLoads(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"yolo": "full"})

	require.Equal(t, YoloModeFull, e.mustLoad(t).Config().Yolo)
}

func TestYolo_GlobalDataPathIsTrusted(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"yolo": "standard"})
	writeConfig(t, e.dataDir, map[string]any{"yolo": "off"})

	require.Equal(t, YoloModeStandard, e.mustLoad(t).Config().Yolo)
}

func TestYolo_ProjectAndWorkspaceIgnored(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"yolo": "standard"})
	writeConfig(t, e.projectDir, map[string]any{"yolo": "full"})
	writeConfig(t, filepath.Join(e.projectDir, ".anvil"), map[string]any{"yolo": "full"})

	store := e.mustLoad(t)
	require.Equal(t, YoloModeStandard, store.Config().Yolo)

	require.NoError(t, store.ReloadFromDisk(context.Background()))
	require.Equal(t, YoloModeStandard, store.Config().Yolo)
}

func TestYolo_ProjectAloneIgnored(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{})
	writeConfig(t, e.projectDir, map[string]any{"yolo": "full"})

	require.Equal(t, YoloMode(""), e.mustLoad(t).Config().Yolo)
}

func TestYolo_InvalidGlobalRejected(t *testing.T) {
	for _, value := range []any{"yes", true} {
		e := newBouncerEnv(t)
		e.writeGlobal(t, map[string]any{"yolo": value})

		_, err := e.load(t)
		require.ErrorContains(t, err, "yolo")
	}
}

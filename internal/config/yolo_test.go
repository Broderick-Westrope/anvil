package config

import (
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

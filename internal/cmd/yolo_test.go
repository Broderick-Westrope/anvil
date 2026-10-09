package cmd

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestResolveYoloLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		noFlag  bool
		cfgYolo config.YoloMode
		want    config.YoloLevel
	}{
		{name: "no flag no config", want: config.YoloOff},
		{name: "config default", cfgYolo: config.YoloModeFull, want: config.YoloFull},
		{name: "bare flag overrides config", args: []string{"--yolo"}, cfgYolo: config.YoloModeFull, want: config.YoloStandard},
		{name: "flag false overrides config", args: []string{"--yolo=false"}, cfgYolo: config.YoloModeStandard, want: config.YoloOff},
		{name: "flag full", args: []string{"--yolo=full"}, want: config.YoloFull},
		{name: "command without flag ignores config", noFlag: true, cfgYolo: config.YoloModeFull, want: config.YoloOff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
			if !tt.noFlag {
				cmd.Flags().StringP("yolo", "y", "", "")
				cmd.Flags().Lookup("yolo").NoOptDefVal = "true"
			}
			require.NoError(t, cmd.ParseFlags(tt.args))

			got, err := resolveYoloLevel(cmd, &config.Config{Yolo: tt.cfgYolo})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

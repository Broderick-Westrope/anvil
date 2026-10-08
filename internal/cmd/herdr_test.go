package cmd

import (
	"os"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/herdr"
	"github.com/stretchr/testify/require"
)

// Not parallel: it uses t.Setenv.
func TestStartHerdrReporter_OutsideHerdr(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv(herdr.EnvReporting, "")

	require.Nil(t, startHerdrReporter())
	require.Empty(t, os.Getenv(herdr.EnvReporting))
}

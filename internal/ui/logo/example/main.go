package main

// This is an example for testing logo treatments. Do not remove.

import (
	"fmt"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/logo"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/term"
)

func main() {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not get terminal size: %s", err)
	}

	s := styles.TokyoNight()
	opts := logo.Opts{
		VersionColor: s.Logo.VersionColor,
		Width:        w,
		RandomColor:  true,
		Unstable:     true,
	}

	lipgloss.Println(logo.Render(s.Logo.GradCanvas, "v1.0.0", true, opts))

	for range 4 {
		lipgloss.Println(logo.Render(s.Logo.GradCanvas, "v1.0.0", false, opts))
	}
}

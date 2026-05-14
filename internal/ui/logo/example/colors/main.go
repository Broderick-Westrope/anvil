// colors renders the ANVIL word in several gradient combinations so you can
// compare them visually before committing to one.
package main

import (
	"fmt"
	"image/color"
	"strings"

	charmtone "github.com/charmbracelet/x/exp/charmtone"

	"github.com/charmbracelet/crush/internal/ui/logo"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

func main() {
	s := styles.TokyoNight()

	type palette struct {
		label string
		a, b  color.Color
	}

	palettes := []palette{
		{"Current (teal→purple)      ", charmtone.Turtle, charmtone.Plum},
		{"Forge fire  (orange→yellow)", charmtone.Tang, charmtone.Mustard},
		{"Hot ember   (red→orange)   ", charmtone.Bengal, charmtone.Yam},
		{"Cold steel  (blue→cyan)    ", charmtone.Sapphire, charmtone.Lichen},
		{"Electric    (blue→green)   ", charmtone.Thunder, charmtone.Julep},
		{"Iron        (gray range)   ", charmtone.Iron, charmtone.Smoke},
		{"Spark       (purple→yellow)", charmtone.Violet, charmtone.Citron},
		{"Lava        (purple→orange)", charmtone.Grape, charmtone.Tang},
	}

	opts := logo.Opts{
		VersionColor: s.Logo.VersionColor,
		Width:        80,
	}

	fmt.Println("── Explicit palettes ──────────────────────────────────────────────────────────")
	for _, p := range palettes {
		opts.TitleColorA = p.a
		opts.TitleColorB = p.b
		fmt.Printf("▸ %s\n", p.label)
		fmt.Println(logo.Render(s.Logo.GradCanvas, "v1.0.0", false, opts))
		fmt.Println(strings.Repeat("─", 80))
	}

	fmt.Println("\n── RandomColor (stable per session) ──────────────────────────────────────────")
	opts.TitleColorA = nil
	opts.TitleColorB = nil
	opts.RandomColor = true
	fmt.Println(logo.Render(s.Logo.GradCanvas, "v1.0.0", false, opts))

	fmt.Println("\n── RandomColor + Unstable (new palette every render) ─────────────────────────")
	opts.Unstable = true
	for range 4 {
		fmt.Println(logo.Render(s.Logo.GradCanvas, "v1.0.0", false, opts))
	}
}

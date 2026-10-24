package styles

import "image/color"

// DiagramPalette is the colours Mermaid diagrams are drawn with. It is
// derived from the theme's palette, so diagrams follow the theme.
type DiagramPalette struct {
	Fg         color.Color // Labels.
	Muted      color.Color // Secondary text.
	Line       color.Color // Edges and arrows.
	Surface    color.Color // Node fill.
	SurfaceAlt color.Color // Cluster and alternate fills.
	Border     color.Color // Node borders.
	Accent     color.Color // Highlights.
	Critical   color.Color // Critical items, e.g. a critical path.
	Error      color.Color // Error states.
	// Series colours distinguish items in charts and pies.
	Series [8]color.Color
}

func diagramPalette(o quickStyleOpts) DiagramPalette {
	return DiagramPalette{
		Fg:         o.fgBase,
		Muted:      o.fgSubtle,
		Line:       o.fgMoreSubtle,
		Surface:    o.bgMostVisible,
		SurfaceAlt: o.bgLessVisible,
		Border:     o.primary,
		Accent:     o.accent,
		Critical:   o.destructive,
		Error:      o.error,
		Series: [8]color.Color{
			o.accent, o.warning, o.success, o.destructive,
			o.secondary, o.warningSubtle, o.primary, o.infoMoreSubtle,
		},
	}
}

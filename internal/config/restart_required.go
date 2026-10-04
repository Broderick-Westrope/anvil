package config

import (
	"cmp"
	"reflect"
)

// RestartRequired names the settings that differ between the startup
// config and the current one but that only a restart applies. It compares
// against startup rather than the previous reload so a pending change stays
// reported until the instance restarts.
//
// Each label was checked against the code that reads it:
//
//   - mcp: the server set is built once by mcp.Initialize. Re-enabling a
//     server and tool filtering read the live config, but running servers
//     are never restarted, so any change is reported.
//   - lsp: lsp.NewManager copies command, args, and the rest into the
//     powernap manager at startup. Only Timeout and Disabled are read
//     live, so any change is reported.
//   - bouncer: buildBouncerOption reads the connection settings (url,
//     model, auth_scheme, api_key_env, timeout_seconds, explicit_ask,
//     send_user_messages) once in app.New. Thresholds are excluded because
//     the reload applies them live; mode is reported separately. Adding or
//     removing the block is reported here too.
//   - bouncer.mode: the runtime mode (ctrl+q) wins over config after
//     startup, so a change is never applied.
//   - options.project_directory: a reload keeps the existing directory,
//     so the raw configured values are compared.
//   - options.tui: only compact_mode and transparent are captured at UI
//     construction. diff_mode and completions are read live, so they are
//     not compared.
func RestartRequired(startup, cur *Config, startupB, curB *TrustedBouncer, startupRaw, curRaw string) []string {
	if startup == nil || cur == nil {
		return nil
	}
	var labels []string
	if !equalMaps(startup.MCP, cur.MCP) {
		labels = append(labels, "mcp")
	}
	if !equalMaps(startup.LSP, cur.LSP) {
		labels = append(labels, "lsp")
	}
	labels = append(labels, bouncerRestartLabels(trustedBouncerConfig(startupB), trustedBouncerConfig(curB))...)
	if startupRaw != curRaw {
		labels = append(labels, "options.project_directory")
	}
	if tuiRestartSettings(startup) != tuiRestartSettings(cur) {
		labels = append(labels, "options.tui")
	}
	return labels
}

func trustedBouncerConfig(tb *TrustedBouncer) *Bouncer {
	if tb == nil {
		return nil
	}
	return tb.Config
}

func bouncerRestartLabels(startup, cur *Bouncer) []string {
	if startup == nil && cur == nil {
		return nil
	}
	if startup == nil || cur == nil {
		return []string{"bouncer"}
	}
	var labels []string
	if !reflect.DeepEqual(bouncerConnection(startup), bouncerConnection(cur)) {
		labels = append(labels, "bouncer")
	}
	if cmp.Or(startup.Mode, BouncerOff) != cmp.Or(cur.Mode, BouncerOff) {
		labels = append(labels, "bouncer.mode")
	}
	return labels
}

// bouncerConnection returns b without the fields a reload handles
// separately (thresholds) or reports separately (mode). Zeroing them rather
// than copying named fields means a newly added field is treated as needing
// a restart until it is shown to be read live.
func bouncerConnection(b *Bouncer) Bouncer {
	c := *b
	c.Mode = ""
	c.EscalateAt = nil
	c.EscalateAtAxes = nil
	c.ConcernAt = nil
	c.SeverityConcern = nil
	c.DenyAt = nil
	c.SeverityEscalate = nil
	c.UserRequestedAt = nil
	return c
}

type tuiRestart struct {
	compactMode bool
	transparent bool
}

func tuiRestartSettings(c *Config) tuiRestart {
	if c.Options == nil || c.Options.TUI == nil {
		return tuiRestart{}
	}
	tui := c.Options.TUI
	return tuiRestart{
		compactMode: tui.CompactMode,
		transparent: tui.Transparent != nil && *tui.Transparent,
	}
}

// equalMaps treats nil and empty maps as equal, since setDefaults
// allocates them only on some paths.
func equalMaps[M ~map[K]V, K comparable, V any](a, b M) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

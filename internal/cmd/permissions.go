package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/decisionlog"
	"github.com/Broderick-Westrope/anvil/internal/permission/triage"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

var permissionsCmd = newPermissionsCmd()

type triageOpts struct {
	Days        int
	MinCount    int
	Scope       string
	JSON        bool
	Yes         bool
	Force       bool
	Interactive bool
	Limit       int
}

type statsOpts struct {
	Days int
	JSON bool
}

func newPermissionsCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "permissions",
		Aliases: []string{"perms"},
		Short:   "Analyze permission decisions and propose explicit rules",
		Long:    "Analyze logged permission decisions. Use triage to propose narrow allow and deny rules from repeated requests, and stats to report request volume and bouncer accuracy.",
	}
	root.AddCommand(newPermissionsTriageCmd())
	root.AddCommand(newPermissionsStatsCmd())
	return root
}

func newPermissionsTriageCmd() *cobra.Command {
	var opts triageOpts
	triageCmd := &cobra.Command{
		Use:   "triage",
		Short: "Propose narrow rules from repeated permission decisions",
		Long:  "Propose narrow rules from repeated permission decisions. Tier A rules come from curated families that are safe for any argument; Tier B rules are uncurated and need review of every argument they permit. --yes applies only Tier A allow rules, never Tier B or deny rules. --scope chooses global or workspace config and evidence. --limit caps displayed rows per section but never what --yes applies. --force writes despite simulation conflicts. --json prints candidates and never writes.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateTriageOpts(opts); err != nil {
				return err
			}
			q, store, cleanup, err := permissionsSetup(cmd)
			if err != nil {
				return err
			}
			defer cleanup()
			in := cmd.InOrStdin()
			if f, ok := in.(*os.File); ok {
				opts.Interactive = term.IsTerminal(f.Fd())
			}
			return runTriage(cmd.Context(), q, store, opts, in, colorprofile.NewWriter(cmd.OutOrStdout(), os.Environ()))
		},
	}
	triageCmd.Flags().IntVar(&opts.Days, "days", 7, "Decision history window in days")
	triageCmd.Flags().IntVar(&opts.MinCount, "min-count", 5, "Minimum repeated decisions")
	triageCmd.Flags().StringVar(&opts.Scope, "scope", "global", "Rule scope: global or workspace")
	triageCmd.Flags().BoolVar(&opts.JSON, "json", false, "Print candidates as JSON without writing")
	triageCmd.Flags().BoolVar(&opts.Yes, "yes", false, "Apply only Tier A allow candidates without prompting")
	triageCmd.Flags().IntVar(&opts.Limit, "limit", 20, "Maximum rows per section in text output (0 for all)")
	triageCmd.Flags().BoolVar(&opts.Force, "force", false, "Write despite reported simulation conflicts")
	return triageCmd
}

func newPermissionsStatsCmd() *cobra.Command {
	var opts statsOpts
	statsCmd := &cobra.Command{
		Use:   "stats",
		Short: "Report permission-request volume and versioned bouncer statistics",
		Long:  "Report permission-request volume by decision source, then per bouncer schema and battery version: shadow comparisons against human verdicts, enforce outcomes, errors, skips, and token and latency usage. Use --json for machine-readable output.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Days <= 0 {
				return errors.New("days must be positive")
			}
			q, _, cleanup, err := permissionsSetup(cmd)
			if err != nil {
				return err
			}
			defer cleanup()
			return runStats(cmd.Context(), q, opts, colorprofile.NewWriter(cmd.OutOrStdout(), os.Environ()))
		},
	}
	statsCmd.Flags().IntVar(&opts.Days, "days", 30, "Decision history window in days")
	statsCmd.Flags().BoolVar(&opts.JSON, "json", false, "Print statistics as JSON")
	return statsCmd
}

func permissionsSetup(cmd *cobra.Command) (db.Querier, *config.ConfigStore, func(), error) {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	cwd, _ := cmd.Flags().GetString("cwd")
	store, err := config.Init(cwd, dataDir, false)
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := db.ConnectGlobal(cmd.Context())
	if err != nil {
		return nil, nil, nil, err
	}
	return db.New(conn), store, func() { _ = db.ReleaseGlobal() }, nil
}

func validateTriageOpts(opts triageOpts) error {
	if opts.Days <= 0 || opts.MinCount <= 0 {
		return errors.New("days and min-count must be positive")
	}
	if opts.Limit < 0 {
		return errors.New("limit must not be negative")
	}
	if opts.Scope != "global" && opts.Scope != "workspace" {
		return errors.New("scope must be global or workspace")
	}
	return nil
}

func permissionRules(store *config.ConfigStore) []config.PermissionRule {
	if p := store.Config().Permissions; p != nil {
		return p.Rules
	}
	return nil
}

// runTriage runs triage and, when it succeeds, records the run so the TUI
// stops nudging the user to triage.
func runTriage(ctx context.Context, q db.Querier, store *config.ConfigStore, opts triageOpts, in io.Reader, out io.Writer) error {
	if err := triagePermissions(ctx, q, store, opts, in, out); err != nil {
		return err
	}
	if err := store.SetLastPermissionTriage(time.Now()); err != nil {
		slog.Warn("Failed to record permission triage run", "error", err)
	}
	return nil
}

func triagePermissions(ctx context.Context, q db.Querier, store *config.ConfigStore, opts triageOpts, in io.Reader, out io.Writer) error {
	if err := validateTriageOpts(opts); err != nil {
		return err
	}
	out = colorWriter(out)
	records, err := decisionlog.LoadRecords(ctx, q, time.Now().AddDate(0, 0, -opts.Days))
	if err != nil {
		return err
	}
	scope := config.ScopeGlobal
	analysisOpts := triage.Options{MinCount: opts.MinCount}
	if opts.Scope == "workspace" {
		scope = config.ScopeWorkspace
		analysisOpts.WorkingDir = store.WorkingDir()
		if analysisOpts.WorkingDir == "" {
			return errors.New("workspace working directory is not set")
		}
		records = slices.DeleteFunc(records, func(r triage.Record) bool { return r.WorkingDir != analysisOpts.WorkingDir })
	}
	rules := permissionRules(store)
	allow, deny := triage.Analyze(records, rules, analysisOpts)
	if opts.JSON {
		return json.NewEncoder(out).Encode(struct {
			Allow []triage.Candidate `json:"allow"`
			Deny  []triage.Candidate `json:"deny"`
		}{allow, deny})
	}
	unresolved := 0
	for _, r := range records {
		if triage.IsSource(r) {
			unresolved++
		}
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "Permission triage: last %d days, %d unresolved requests\n", opts.Days, unresolved)
	candidates := writeTriageSections(&buf, allow, deny, opts.Limit)
	if _, err := io.WriteString(out, buf.String()); err != nil {
		return err
	}
	if len(candidates) == 0 || (!opts.Interactive && !opts.Yes) {
		return nil
	}
	selection := "a"
	if !opts.Yes {
		if _, err := fmt.Fprint(out, "\nSelect rules to add (e.g. 1,2,5; a for all tier A; blank to cancel): "); err != nil {
			return err
		}
		scanner := bufio.NewScanner(in)
		if !scanner.Scan() {
			return scanner.Err()
		}
		selection = scanner.Text()
	}
	chosen, err := selectPermissionCandidates(candidates, allow, selection)
	if err != nil || len(chosen) == 0 {
		return err
	}
	return applyPermissionCandidates(ctx, store, scope, rules, chosen, records, opts.Force, out)
}

type triageSection struct {
	title, warning string
	candidates     []triage.Candidate
}

// writeTriageSections renders each section as an aligned table capped at
// limit rows (0 for no cap) and returns the displayed candidates in the
// order they are numbered.
func writeTriageSections(out io.Writer, allow, deny []triage.Candidate, limit int) []triage.Candidate {
	tierA := slices.DeleteFunc(slices.Clone(allow), func(c triage.Candidate) bool { return c.Tier != triage.TierA })
	tierB := slices.DeleteFunc(slices.Clone(allow), func(c triage.Candidate) bool { return c.Tier == triage.TierA })
	sections := []triageSection{
		{"Suggested allow rules (tier A: curated safe families)", "", tierA},
		{"Needs your judgment (tier B)", "Tier B: uncurated patterns — review every argument each one permits; --yes never applies these.", tierB},
		{"Suggested deny rules", "Review the scope of each permanent denial.", deny},
	}
	heading := lipgloss.NewStyle().Foreground(charmtone.Malibu).Bold(true)
	var displayed []triage.Candidate
	for _, s := range sections {
		if len(s.candidates) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s\n", heading.Render(s.title))
		if s.warning != "" {
			fmt.Fprintln(out, s.warning)
		}
		shown := s.candidates
		if limit > 0 && len(shown) > limit {
			shown = shown[:limit]
		}
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "#\tcount\tsess\tproj\trule\texamples")
		for _, c := range shown {
			displayed = append(displayed, c)
			fmt.Fprintf(tw, "%d\t%d\t%d\t%d\t%s: %q\t%s\n", len(displayed), c.Count, c.Sessions, c.Projects, terminalText(c.ToolPattern), terminalText(c.InputPattern), exampleSummary(c.Examples[:min(2, len(c.Examples))]))
			if c.InputPattern == "" && c.Warning != "" {
				fmt.Fprintf(tw, "\t\t\t\t\t%s\n", terminalText(c.Warning))
			}
		}
		_ = tw.Flush()
		if hidden := len(s.candidates) - len(shown); hidden > 0 {
			fmt.Fprintf(out, "… %d more (use --limit 0 to show all)\n", hidden)
		}
	}
	if len(displayed) == 0 {
		fmt.Fprintln(out, "No candidate rules.")
	}
	return displayed
}

// selectPermissionCandidates resolves numbers against the displayed rows,
// while "a" takes every Tier A allow candidate so --limit never hides one.
func selectPermissionCandidates(candidates, allow []triage.Candidate, selection string) ([]triage.Candidate, error) {
	selection = strings.TrimSpace(selection)
	var chosen []triage.Candidate
	if selection == "" {
		return chosen, nil
	}
	if selection == "a" {
		for _, c := range allow {
			if c.Tier == triage.TierA && c.Kind == triage.KindAllow {
				chosen = append(chosen, c)
			}
		}
		return chosen, nil
	}
	seen := map[int]bool{}
	manual := 0
	for _, token := range strings.Split(selection, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(token))
		if err != nil || n < 1 || n > len(candidates) {
			return nil, fmt.Errorf("invalid rule selection %q", token)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		c := candidates[n-1]
		if c.Kind == triage.KindAllow && c.Tier == triage.TierB {
			manual++
		}
		chosen = append(chosen, c)
	}
	if manual > 1 {
		return nil, errors.New("select Tier B allow rules one at a time")
	}
	return chosen, nil
}

func applyPermissionCandidates(ctx context.Context, store *config.ConfigStore, scope config.Scope, rules []config.PermissionRule, chosen []triage.Candidate, records []triage.Record, force bool, out io.Writer) error {
	conflicts := triage.Simulate(rules, chosen, records)
	for _, c := range conflicts {
		if _, err := fmt.Fprintf(out, "Conflict: %s %q: %s\n", terminalText(c.ToolName), terminalText(c.Input), c.Reason); err != nil {
			return err
		}
	}
	if len(conflicts) > 0 && !force {
		return errors.New("simulation conflicts; no rules written (use --force to override)")
	}
	path, err := store.PermissionConfigPath(scope)
	if err != nil {
		return err
	}
	for _, c := range chosen {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store.SetPermissionRule(scope, c.ToolPattern, c.InputPattern, config.PermissionAction(c.Kind)); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var written struct {
		Permissions config.Permissions `json:"permissions"`
	}
	if err := json.Unmarshal(data, &written); err != nil {
		return fmt.Errorf("reading written permissions: %w", err)
	}
	updated := written.Permissions.Rules
	if store.WorkingDir() != "" {
		if err := store.ReloadFromDisk(ctx); err != nil {
			return fmt.Errorf("rules written but config reload failed: %w", err)
		}
		updated = permissionRules(store)
	}
	failed := false
	for _, c := range chosen {
		ok := true
		for _, example := range c.Examples {
			if permission.Evaluate(c.ToolPattern, example, updated, nil).Action != config.PermissionAction(c.Kind) {
				ok = false
				if _, err := fmt.Fprintf(out, "FAIL example: %s %q does not evaluate to %s\n", terminalText(c.ToolPattern), terminalText(example), c.Kind); err != nil {
					return err
				}
			}
		}
		status := "OK"
		if !ok {
			status = "FAIL"
			failed = true
		}
		if _, err := fmt.Fprintf(out, "%s %s: %q -> %s\n", status, terminalText(c.ToolPattern), terminalText(c.InputPattern), c.Kind); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "Config: %s\n", terminalText(path)); err != nil {
		return err
	}
	if failed {
		return errors.New("rules written but some examples do not evaluate to the intended action")
	}
	return nil
}

// colorWriter strips or downsamples ANSI styling unless w already applies
// a colour profile, so piped or buffered output stays plain.
func colorWriter(w io.Writer) io.Writer {
	if _, ok := w.(*colorprofile.Writer); ok {
		return w
	}
	return colorprofile.NewWriter(w, os.Environ())
}

func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func exampleSummary(examples []string) string {
	out := make([]string, len(examples))
	for i, example := range examples {
		runes := []rune(terminalText(example))
		if len(runes) > 60 {
			runes = append(runes[:59], '…')
		}
		out[i] = string(runes)
	}
	return strings.Join(out, " | ")
}

func runStats(ctx context.Context, q db.Querier, opts statsOpts, out io.Writer) error {
	if opts.Days <= 0 {
		return errors.New("days must be positive")
	}
	out = colorWriter(out)
	rows, err := q.ListPermissionDecisionsSince(ctx, time.Now().AddDate(0, 0, -opts.Days).Unix())
	if err != nil {
		return err
	}
	stats := decisionlog.ComputeStats(rows)
	if opts.JSON {
		return json.NewEncoder(out).Encode(stats)
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "Permission-request volume: %d (last %d days)\n", stats.Total, opts.Days)
	writeCounts(&buf, "Decided by", stats.ByDecidedBy)
	fmt.Fprintf(&buf, "Without assessment: %d; invalid assessments: %d\n", stats.WithoutAssessment, stats.InvalidAssessments)
	if len(stats.Groups) == 0 {
		fmt.Fprintln(&buf, "Shadow: 0 samples\nEnforce: 0 samples\nWarning: not enough evidence to enable enforce")
	}
	for _, g := range stats.Groups {
		fmt.Fprintf(&buf, "\nSchema %d / battery %q: %d requests\n", g.SchemaVersion, terminalText(g.BatteryVersion), g.Total)
		writeCounts(&buf, "Decided by", g.ByDecidedBy)
		fmt.Fprintf(&buf, "Shadow: %d samples\n", g.Shadow.Samples)
		writeMatrix(&buf, g.Shadow.Matrix)
		alert := fmt.Sprintf("Shadow bouncer allow x human deny: %d", g.Shadow.Matrix["allow"]["deny"])
		fmt.Fprintln(&buf, lipgloss.NewStyle().Foreground(charmtone.Coral).Bold(true).Render(alert))
		comparisons := 0
		for _, outcome := range []string{"allow", "escalate", "deny"} {
			comparisons += g.Shadow.Matrix[outcome]["allow"] + g.Shadow.Matrix[outcome]["deny"]
		}
		if comparisons < 200 {
			fmt.Fprintln(&buf, "Warning: not enough evidence to enable enforce (fewer than 200 shadow comparisons)")
		}
		fmt.Fprintf(&buf, "Enforce: %d samples\n", g.Enforce.Samples)
		writeCounts(&buf, "Bouncer verdicts", g.Enforce.Bouncer)
		fmt.Fprintf(&buf, "Human resolutions: %d\n", g.Enforce.Human.Samples)
		writeMatrix(&buf, g.Enforce.Human.Matrix)
		writeCounts(&buf, "Errors", g.Errors)
		writeCounts(&buf, "Skips", g.Skips)
		u := g.Usage
		fmt.Fprintf(&buf, "Usage (%d valid outcomes): tokens input=%d output=%d; mean input=%.2f output=%.2f; latency mean=%.2fms p95=%dms\n", u.Samples, u.InputTokens, u.OutputTokens, u.MeanInputTokens, u.MeanOutputTokens, u.MeanLatencyMS, u.P95LatencyMS)
	}
	_, err = io.WriteString(out, buf.String())
	return err
}

func writeCounts(out io.Writer, title string, counts map[string]int) {
	fmt.Fprintf(out, "%s:", title)
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		fmt.Fprintf(out, " %s=%d", terminalText(key), counts[key])
	}
	fmt.Fprintln(out)
}

func writeMatrix(out io.Writer, matrix decisionlog.VerdictMatrix) {
	for _, outcome := range slices.Sorted(maps.Keys(matrix)) {
		writeCounts(out, "  "+terminalText(outcome), matrix[outcome])
	}
}

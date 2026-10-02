package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/decisionlog"
	"github.com/Broderick-Westrope/anvil/internal/permission/triage"
)

func permissionsTestDB(t *testing.T) *db.Queries {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dir)) })
	return db.New(conn)
}

func seedPermissions(t *testing.T, q db.Querier) {
	t.Helper()
	for _, input := range []string{"git status --short", "wc -l out.txt", "gh pr merge 42"} {
		verdict := "allow"
		if strings.Contains(input, "merge") {
			verdict = "deny"
		}
		for i := range 6 {
			require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{ID: fmt.Sprintf("%s-%d", input, i), ToolName: "bash", Input: input, InputSegments: "[]", WorkingDir: "/project", SessionID: "session", DecidedBy: "human", Verdict: verdict}))
		}
	}
}

func TestPermissionsTriage(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                           string
		yes, json, interactive, writes bool
		selection                      string
	}{
		{"interactive", false, false, true, true, "1\n"},
		{"yes", true, false, false, true, ""},
		{"json overrides yes", true, true, true, false, "1\n"},
		{"non tty", false, false, false, false, "1\n"},
		{"cancel", false, false, true, false, "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q := permissionsTestDB(t)
			seedPermissions(t, q)
			path := filepath.Join(t.TempDir(), "anvil.json")
			store := config.NewTestStoreWithDataPath(&config.Config{}, path)
			var out bytes.Buffer
			err := runTriage(t.Context(), q, store, triageOpts{Days: 7, MinCount: 5, Scope: "global", Yes: tt.yes, JSON: tt.json, Interactive: tt.interactive}, strings.NewReader(tt.selection), &out)
			require.NoError(t, err)
			data, err := os.ReadFile(path)
			if !tt.writes {
				require.ErrorIs(t, err, os.ErrNotExist)
				if tt.json {
					var result struct{ Allow, Deny []triage.Candidate }
					require.NoError(t, json.Unmarshal(out.Bytes(), &result))
					require.Len(t, result.Allow, 2)
					require.Len(t, result.Deny, 1)
				}
				return
			}
			require.NoError(t, err)
			var cfg struct {
				Permissions config.Permissions `json:"permissions"`
			}
			require.NoError(t, json.Unmarshal(data, &cfg))
			require.Equal(t, config.PermissionAllow, permission.Evaluate("bash", "git status", cfg.Permissions.Rules, nil).Action)
			require.Equal(t, config.PermissionAsk, permission.Evaluate("bash", "wc -l out.txt", cfg.Permissions.Rules, nil).Action)
			require.Equal(t, config.PermissionAsk, permission.Evaluate("bash", "gh pr merge 42", cfg.Permissions.Rules, nil).Action)
			require.Contains(t, out.String(), `OK bash: "git status *" -> allow`)
			require.Contains(t, out.String(), path)
		})
	}
}

func TestPermissionsConflicts(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "anvil.json")
			store := config.NewTestStoreWithDataPath(&config.Config{}, path)
			chosen := []triage.Candidate{{Kind: triage.KindAllow, Tier: triage.TierB, ToolPattern: "bash", InputPattern: "tee *", Examples: []string{"tee out.txt"}}}
			evidence := []triage.Record{{ToolName: "bash", Input: "tee /etc/hosts", DecidedBy: "human", Verdict: "deny"}}
			var out bytes.Buffer
			err := applyPermissionCandidates(t.Context(), store, config.ScopeGlobal, nil, chosen, evidence, force, &out)
			require.Contains(t, out.String(), "Conflict:")
			if force {
				require.NoError(t, err)
				require.FileExists(t, path)
			} else {
				require.ErrorContains(t, err, "no rules written")
				require.NoFileExists(t, path)
			}
		})
	}
}

func TestPermissionsSelection(t *testing.T) {
	t.Parallel()
	candidates := []triage.Candidate{{Kind: triage.KindAllow, Tier: triage.TierA}, {Kind: triage.KindAllow, Tier: triage.TierB}, {Kind: triage.KindDeny, Tier: triage.TierB}, {Kind: triage.KindAllow, Tier: triage.TierB}}
	chosen, err := selectPermissionCandidates(candidates, "a")
	require.NoError(t, err)
	require.Equal(t, candidates[:1], chosen)
	chosen, err = selectPermissionCandidates(candidates, "1,2,3,1")
	require.NoError(t, err)
	require.Len(t, chosen, 3)
	for _, selection := range []string{"0", "5", "oops", "2,4", "1,"} {
		_, err := selectPermissionCandidates(candidates, selection)
		require.Error(t, err)
	}
	require.Len(t, []rune(exampleSummary([]string{strings.Repeat("界", 100)})), 60)
	require.NotContains(t, terminalText("\x1b[2J\n"), "\x1b")
}

func TestPermissionsStats(t *testing.T) {
	t.Parallel()
	q := permissionsTestDB(t)
	for i, a := range []permission.AssessmentRecord{
		{SchemaVersion: 1, BatteryVersion: "v1", Mode: "shadow", Outcome: "allow"},
		{SchemaVersion: 1, BatteryVersion: "v2", Mode: "shadow", Outcome: "allow"},
		{SchemaVersion: 1, BatteryVersion: "v1", Mode: "enforce", Outcome: "escalate"},
	} {
		data, err := json.Marshal(a)
		require.NoError(t, err)
		require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{ID: fmt.Sprint(i), DecidedBy: "human", Verdict: "deny", InputSegments: "[]", Assessment: sql.NullString{Valid: true, String: string(data)}}))
	}
	var out bytes.Buffer
	require.NoError(t, runStats(t.Context(), q, statsOpts{Days: 30, JSON: true}, &out))
	var stats decisionlog.Stats
	require.NoError(t, json.Unmarshal(out.Bytes(), &stats))
	require.Len(t, stats.Groups, 2)
	require.Equal(t, 1, stats.Groups[0].Shadow.Matrix["allow"]["deny"])
	require.Equal(t, 1, stats.Groups[0].Enforce.Human.Matrix["escalate"]["deny"])
	out.Reset()
	require.NoError(t, runStats(t.Context(), q, statsOpts{Days: 30}, &out))
	require.Contains(t, out.String(), "Permission-request volume: 3")
	require.Contains(t, out.String(), "not enough evidence to enable enforce")
	require.Contains(t, out.String(), "assessor allow x human deny: 1")
}

func TestPermissionsStatsNoAssessments(t *testing.T) {
	t.Parallel()
	q := permissionsTestDB(t)
	seedPermissions(t, q)
	var out bytes.Buffer
	require.NoError(t, runStats(t.Context(), q, statsOpts{Days: 30}, &out))
	require.Contains(t, out.String(), "Shadow: 0 samples")
	require.Contains(t, out.String(), "Enforce: 0 samples")
}

func TestPermissionsWorkspace(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("ANVIL_GLOBAL_CONFIG", global)
	t.Setenv("ANVIL_GLOBAL_DATA", global)
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	require.NoError(t, os.WriteFile(filepath.Join(project, "anvil.json"), []byte(`{"models":{"large":{"provider":"test","model":"test"},"small":{"provider":"test","model":"test"}},"providers":{"test":{"type":"openai","api_key":"test","base_url":"http://127.0.0.1:1","models":[{"id":"test","name":"Test","context_window":10000}]}},"options":{"disable_default_providers":true,"disable_provider_auto_update":true}}`), 0o600))
	store, err := config.Load(project, filepath.Join(project, ".anvil"), false)
	require.NoError(t, err)
	q := permissionsTestDB(t)
	for i := range 6 {
		require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{ID: fmt.Sprint(i), ToolName: "bash", Input: "git status", InputSegments: "[]", WorkingDir: project, Verdict: "allow", DecidedBy: "session_rule"}))
	}
	require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{ID: "other", ToolName: "bash", Input: "git status", InputSegments: "[]", WorkingDir: "/other", Verdict: "deny", DecidedBy: "rule"}))
	var out bytes.Buffer
	require.NoError(t, runTriage(t.Context(), q, store, triageOpts{Days: 7, MinCount: 5, Scope: "workspace", Interactive: true}, strings.NewReader("1\n"), &out))
	path := filepath.Join(project, ".anvil", "anvil.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var cfg struct {
		Permissions config.Permissions `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.Equal(t, config.PermissionAllow, permission.Evaluate("bash", "git status", cfg.Permissions.Rules, nil).Action)
	require.Equal(t, config.PermissionAllow, permission.Evaluate("bash", "git status", permissionRules(store), nil).Action)
	require.Contains(t, out.String(), "6 unresolved requests")
	require.Contains(t, out.String(), path)
	require.NoFileExists(t, filepath.Join(global, "anvil.json"))
}

func TestPermissionsHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--help"}, {"triage", "--help"}, {"stats", "--help"}} {
		cmd := newPermissionsCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute())
		require.Contains(t, out.String(), "permissions")
	}
}

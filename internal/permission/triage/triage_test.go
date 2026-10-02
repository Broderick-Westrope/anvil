package triage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/config"
)

func repeated(input, verdict string) []Record {
	var records []Record
	for i := range 6 {
		records = append(records, Record{SessionID: fmt.Sprint(i % 2), WorkingDir: fmt.Sprint(i % 3), ToolName: "bash", Input: input, DecidedBy: "human", Verdict: verdict})
	}
	return records
}

func TestAnalyze(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, verdict, pattern string
		tier                          Tier
		deny                          bool
	}{
		{"curated", "git status --short", "allow", "git status *", TierA, false},
		{"go test uncurated", "go test ./...", "allow", "go test *", TierB, false},
		{"git global options", "git -C /x status", "allow", "", TierB, false},
		{"find", "find . -name x", "allow", "find *", TierB, false},
		{"verb-specific denial", "gh pr merge 42", "deny", "gh pr merge *", TierB, true},
		{"denied chain", "git status && gh pr merge 1", "deny", "", TierB, true},
		{"denied redirect", "git status > /etc/hosts", "deny", "", TierB, true},
		{"metacharacters", "echo '$SECRET'", "allow", "", TierB, false},
		{"wildcard executable", "* arg", "allow", "", TierB, false},
		{"wildcard subcommand", "foo? arg", "allow", "", TierB, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, d := Analyze(repeated(tt.input, tt.verdict), nil, Options{})
			got := a
			if tt.deny {
				got = d
				require.Empty(t, a)
			} else {
				require.Empty(t, d)
			}
			if tt.pattern == "" {
				require.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			require.Equal(t, tt.pattern, got[0].InputPattern)
			require.Equal(t, tt.tier, got[0].Tier)
			require.Equal(t, 6, got[0].Count)
			require.Equal(t, 2, got[0].Sessions)
			require.Equal(t, 3, got[0].Projects)
		})
	}
}

func TestAnalyzeEvidence(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"human", "assessor", "rule", "hook", "yolo", "session_rule", "session_grant"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			records := append(repeated("cat file.txt", "allow"), Record{ToolName: "bash", Input: "cat .env", Verdict: "deny", DecidedBy: source})
			a, d := Analyze(records, nil, Options{})
			require.Empty(t, a)
			require.Empty(t, d)
		})
	}
	a, _ := Analyze(append(repeated("gh pr view 1", "allow"), Record{ToolName: "bash", Input: "gh pr merge 1", Verdict: "deny", DecidedBy: "human"}), nil, Options{})
	require.Len(t, a, 1)
	require.Equal(t, "gh pr view *", a[0].InputPattern)
	require.Equal(t, TierB, a[0].Tier)
	a, d := Analyze(append(repeated("npm install", "allow")[:5], Record{ToolName: "bash", Input: "npm install", Verdict: "deny", DecidedBy: "human"}), nil, Options{})
	require.Empty(t, a)
	require.Empty(t, d)
	a, _ = Analyze(repeated("go test ./... | tee out.txt", "allow"), nil, Options{})
	require.Len(t, a, 2)
	require.Equal(t, "go test *", a[0].InputPattern)
	require.Equal(t, "tee *", a[1].InputPattern)
}

func TestAnalyzeFilters(t *testing.T) {
	t.Parallel()
	for _, action := range []config.PermissionAction{config.PermissionAllow, config.PermissionDeny} {
		rules := config.UpsertPermissionRule(nil, "bash", "go test *", action)
		a, _ := Analyze(repeated("go test ./...", "allow"), rules, Options{})
		require.Empty(t, a)
	}
	records := repeated("git status", "allow")
	a, _ := Analyze(records, nil, Options{WorkingDir: "0"})
	require.Empty(t, a)
	a, _ = Analyze(records, nil, Options{WorkingDir: "0", MinCount: 2})
	require.Len(t, a, 1)
	require.Equal(t, 1, a[0].Projects)
	hazard := 0.3
	records[0].MaxHazard = &hazard
	a, _ = Analyze(records, nil, Options{})
	require.Empty(t, a)
	for i := range records {
		records[i].ToolName = "mcp_Linear_get_issue"
		records[i].MaxHazard = nil
	}
	a, _ = Analyze(records, nil, Options{})
	require.Len(t, a, 1)
	require.Equal(t, TierB, a[0].Tier)
	require.Equal(t, "", a[0].InputPattern)
	require.Contains(t, a[0].Warning, "every argument")
	for i := range records {
		records[i].ToolName = "edit"
	}
	a, _ = Analyze(records, nil, Options{})
	require.Empty(t, a)
}

func TestAnalyzeChecksBeyondDisplayedExamples(t *testing.T) {
	t.Parallel()
	records := repeated("git status", "allow")
	for i := range records {
		records[i].Input = fmt.Sprintf("git status --porcelain=v%d", i)
	}
	rules := config.UpsertPermissionRule(nil, "bash", records[5].Input, config.PermissionDeny)
	a, _ := Analyze(records, rules, Options{})
	require.Empty(t, a)
}

func TestSimulate(t *testing.T) {
	t.Parallel()
	chosen := []Candidate{{Kind: KindAllow, ToolPattern: "bash", InputPattern: "tee *", Examples: []string{"tee out.txt"}}}
	conflicts := Simulate(nil, chosen, []Record{{ToolName: "bash", Input: "tee /etc/hosts", Verdict: "deny", DecidedBy: "human"}})
	require.Len(t, conflicts, 1)
	rules := config.UpsertPermissionRule(nil, "bash", "tee *", config.PermissionAsk)
	rules = append(rules, config.PermissionRule{ToolPattern: "*", Action: config.PermissionDeny})
	conflicts = Simulate(rules, chosen, nil)
	require.Len(t, conflicts, 1)
	require.Equal(t, config.PermissionAsk, rules[0].SubRules[0].Action)
	require.Empty(t, Simulate(nil, chosen, []Record{{ToolName: "bash", Input: "tee /etc/hosts && rm /etc/hosts", Verdict: "deny", DecidedBy: "human"}}))
}

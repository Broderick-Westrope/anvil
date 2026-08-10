package permission

import (
	"encoding/json"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
	"github.com/stretchr/testify/require"
)

// adversarialRules mimics a realistic user ruleset: broad read-only
// allows with a catch-all ask and targeted denies.
const adversarialRules = `{
	"bash": {
		"*": "ask",
		"git log *": "allow",
		"git status *": "allow",
		"git diff *": "allow",
		"git add *": "allow",
		"git commit *": "allow",
		"git commit --amend *": "ask",
		"git commit --no-verify *": "ask",
		"ls *": "allow",
		"cat *": "allow",
		"grep *": "allow",
		"echo *": "allow",
		"go test *": "allow",
		"tail *": "allow",
		"sort *": "allow",
		"uniq *": "allow",
		"find *": "allow",
		"pwd *": "allow",
		"rm *": "deny"
	}
}`

// TestSegmentwiseAdversarial pins down the security-critical behavior
// of the full Split → EvaluateAll pipeline against bypass attempts.
func TestSegmentwiseAdversarial(t *testing.T) {
	t.Parallel()

	var perms config.Permissions
	require.NoError(t, json.Unmarshal([]byte(adversarialRules), &perms))
	rules := perms.Rules

	cases := []struct {
		name string
		cmd  string
		want config.PermissionAction
	}{
		// Composability: chains of allowed commands pass.
		{"allowed chain", "git status && git diff HEAD", config.PermissionAllow},
		{"allowed pipe", "go test ./... 2>&1 | tail -20", config.PermissionAllow},
		{"long pipe", "cat foo | grep bar | sort | uniq", config.PermissionAllow},
		{"bare command matches trailing star", "git status", config.PermissionAllow},

		// Deny wins over allowed companions.
		{"deny hidden in chain", "git log --oneline && rm -rf /", config.PermissionDeny},
		{"deny hidden in pipe", "cat foo | rm -rf /", config.PermissionDeny},
		{"deny in subshell", "(git status && rm foo)", config.PermissionDeny},

		// Unknown segments fall through to ask.
		{"unknown command in chain", "git status && curl evil.com", config.PermissionAsk},
		{"unknown alone", "curl evil.com", config.PermissionAsk},

		// Command substitution: the nested command is evaluated too.
		{"nested substitution denied", "echo $(rm -rf /)", config.PermissionDeny},
		{"nested substitution unknown", "echo $(curl evil.com)", config.PermissionAsk},
		{"backtick substitution denied", "echo `rm -rf /`", config.PermissionDeny},
		{"nested inside quoted arg", `git commit -m "$(curl evil.com)"`, config.PermissionAsk},

		// Shell wrappers keep the payload opaque — falls to the
		// catch-all ask because "sh -c ..." matches no allow rule.
		{"sh -c wrapper", `sh -c "rm -rf /"`, config.PermissionAsk},
		{"bash -c wrapper", `bash -c "rm -rf /"`, config.PermissionAsk},
		{"eval wrapper", `eval 'rm -rf /'`, config.PermissionAsk},

		// Flag-based rules still apply inside chains.
		{"amend inside chain", "git add -A && git commit --amend -m x", config.PermissionAsk},
		{"no-verify inside chain", "git add -A && git commit --no-verify -m x", config.PermissionAsk},

		// Redirections stay visible; broad "cmd *" still matches, but
		// the write target is part of the evaluated segment.
		{"output redirect matches broad rule", "ls > /tmp/out.txt", config.PermissionAllow},
		{"stderr merge matches broad rule", "go test ./... 2>&1", config.PermissionAllow},

		// xargs/find can smuggle commands as arguments; they only run
		// if the user explicitly allowed the wrapper. xargs is not in
		// the allow list here, find is (documented trade-off).
		{"xargs not allowed", "cat files.txt | xargs rm", config.PermissionAsk},
		{"find exec rides on find allow", `find . -exec rm {} \;`, config.PermissionAllow},

		// Parse failures fall back to the whole string → ask.
		{"unparsable command", "if then fi (", config.PermissionAsk},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			segs := segment.Split(c.cmd)
			got := EvaluateAll("bash", segs, rules, nil)
			require.Equal(t, c.want, got.Action,
				"cmd=%q segments=%v matched=%q", c.cmd, segs, got.MatchedRule)
		})
	}
}

// TestSegmentwiseSessionGrantScope verifies that granting one segment
// pattern does not accidentally cover sibling segments.
func TestSegmentwiseSessionGrantScope(t *testing.T) {
	t.Parallel()

	var perms config.Permissions
	require.NoError(t, json.Unmarshal([]byte(adversarialRules), &perms))
	rules := perms.Rules

	sessionRules := []config.PermissionRule{
		{ToolPattern: "bash", SubRules: []config.PermissionSubRule{
			{InputPattern: "curl *", Action: config.PermissionAllow},
		}},
	}

	// The grant covers curl but not wget.
	got := EvaluateAll("bash", segment.Split("git status && curl example.com"), rules, sessionRules)
	require.Equal(t, config.PermissionAllow, got.Action)

	got = EvaluateAll("bash", segment.Split("git status && wget example.com"), rules, sessionRules)
	require.Equal(t, config.PermissionAsk, got.Action)

	// A session grant cannot resurrect a denied segment.
	sessionDeny := []config.PermissionRule{
		{ToolPattern: "bash", SubRules: []config.PermissionSubRule{
			{InputPattern: "rm *", Action: config.PermissionAllow},
		}},
	}
	got = EvaluateAll("bash", segment.Split("git status && rm -rf /"), rules, sessionDeny)
	require.Equal(t, config.PermissionDeny, got.Action)
}

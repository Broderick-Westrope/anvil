package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	maxTabLabel   = 30
	tabSyncBudget = time.Second
)

// TabLabel turns a session title into a tab label: control characters
// removed, whitespace collapsed, capped with an ellipsis. Herdr has no
// length limit and renders control characters badly.
func TabLabel(title string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	label := strings.Join(strings.Fields(cleaned), " ")
	if runes := []rune(label); len(runes) > maxTabLabel {
		label = string(runes[:maxTabLabel-1]) + "…"
	}
	return label
}

// tabNamer names the pane's tab after the session title. It is owned by
// the reporter loop and needs no locking; Close uses it only after the
// loop has exited.
type tabNamer struct {
	pane      string
	ours      map[string]string // Tab ID to the label Anvil set.
	userNamed map[string]bool   // Tabs the user renamed; never touched again.
	synced    string            // Title fully handled (renamed or decided against).
	dirty     bool              // A re-check is owed (title change or idle/blocked edge).
}

func newTabNamer(pane string) tabNamer {
	return tabNamer{
		pane:      pane,
		ours:      map[string]string{},
		userNamed: map[string]bool{},
	}
}

type tabInfo struct {
	TabID     string `json:"tab_id"`
	Label     string `json:"label"`
	Number    int    `json:"number"`
	PaneCount int    `json:"pane_count"`
}

// sync renames the pane's tab to the session title when Anvil may claim
// it. It returns an error when a herdr command failed and a retry is owed.
func (t *tabNamer) sync(ctx context.Context, run runner, s State, statusChanged bool) error {
	if s.SessionTitle != t.synced {
		t.dirty = true
	}
	if statusChanged && (s.Status == StatusIdle || s.Status == StatusBlocked) {
		t.dirty = true
	}
	want := TabLabel(s.SessionTitle)
	if !t.dirty || want == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, tabSyncBudget)
	defer cancel()

	if err := t.claim(ctx, run, want); err != nil {
		return fmt.Errorf("name tab: %w", err)
	}
	t.synced = s.SessionTitle
	t.dirty = false
	return nil
}

func (t *tabNamer) claim(ctx context.Context, run runner, want string) error {
	tabID, err := paneTab(ctx, run, t.pane)
	if err != nil {
		return err
	}
	if t.userNamed[tabID] {
		return nil
	}
	tab, err := getTab(ctx, run, tabID)
	if err != nil {
		return err
	}
	if tab.PaneCount != 1 {
		return nil
	}
	if !isDigits(tab.Label) && tab.Label != t.ours[tabID] {
		t.userNamed[tabID] = true
		slog.Debug("Herdr tab named by user; leaving it alone", "tab", tabID)
		return nil
	}
	if tab.Label != want {
		if _, err := run.run(ctx, "tab", "rename", tabID, want); err != nil {
			return err
		}
	}
	t.ours[tabID] = want
	return nil
}

// restore puts each tab Anvil still names back to its position number.
// Herdr cannot restore an automatic label, and an empty label would leave
// a blank tab.
func (t *tabNamer) restore(ctx context.Context, run runner) {
	for tabID, label := range t.ours {
		tab, err := getTab(ctx, run, tabID)
		if err != nil {
			slog.Debug("Herdr tab lookup for restore failed", "tab", tabID, "error", err)
			continue
		}
		if tab.Label != label {
			continue
		}
		if _, err := run.run(ctx, "tab", "rename", tabID, strconv.Itoa(tab.Number)); err != nil {
			slog.Debug("Herdr tab restore failed", "tab", tabID, "error", err)
		}
	}
}

func paneTab(ctx context.Context, run runner, pane string) (string, error) {
	out, err := run.run(ctx, "pane", "get", pane)
	if err != nil {
		return "", err
	}
	var resp struct {
		Result struct {
			Pane struct {
				TabID string `json:"tab_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("parse herdr pane get: %w", err)
	}
	if resp.Result.Pane.TabID == "" {
		return "", fmt.Errorf("herdr pane get: no tab_id")
	}
	return resp.Result.Pane.TabID, nil
}

func getTab(ctx context.Context, run runner, tabID string) (tabInfo, error) {
	out, err := run.run(ctx, "tab", "get", tabID)
	if err != nil {
		return tabInfo{}, err
	}
	var resp struct {
		Result struct {
			Tab tabInfo `json:"tab"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return tabInfo{}, fmt.Errorf("parse herdr tab get: %w", err)
	}
	return resp.Result.Tab, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

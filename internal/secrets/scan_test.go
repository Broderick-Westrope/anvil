package secrets_test

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/secrets"
	"github.com/stretchr/testify/require"
)

// TestSecretsScan replays the detector over the messages in a real Anvil
// database, to tune the rules against real history before and after
// masking ships on by default. It reads a copy, so the database itself is
// never opened, and prints only masked text. It only runs when asked:
//
//	ANVIL_SECRETS_SCAN=1 go test ./internal/secrets -run TestSecretsScan -v
//
// ANVIL_SECRETS_SCAN_DB points it at a database other than the global one.
// For each rule it prints how many findings there were, how many per 1,000
// tool results, and a sample of up to 20 excerpts with the match masked,
// for a hand review of false alarms against the per-rule ship bar. The
// context around a match can hold a secret no rule caught, which is what
// the scan looks for, so keep its output on your machine.
func TestSecretsScan(t *testing.T) {
	if os.Getenv("ANVIL_SECRETS_SCAN") == "" {
		t.Skip("set ANVIL_SECRETS_SCAN=1 to scan your Anvil history")
	}
	source := cmp.Or(os.Getenv("ANVIL_SECRETS_SCAN_DB"), filepath.Join(config.GlobalDataDir(), "anvil.db"))
	dir := t.TempDir()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		copyIfExists(t, source+suffix, filepath.Join(dir, "anvil.db"+suffix))
	}
	conn, err := db.Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	rows, err := conn.QueryContext(t.Context(), "SELECT parts FROM messages")
	require.NoError(t, err)
	defer rows.Close()

	d := secrets.New(nil, []byte("secrets scan key, not a real one"))
	d.AddKnown(secrets.EnvKnown(os.Environ())...)
	scan := newScanReport()
	for rows.Next() {
		var parts string
		require.NoError(t, rows.Scan(&parts))
		for _, part := range scanTexts(t, parts, scan) {
			// Mask each part as the model request does: tool calls and
			// JSON tool output as JSON, the rest as text.
			mask := d.Mask
			if part.json {
				mask = d.MaskJSON
			}
			masked, findings := mask(part.text)
			scan.add(part.text, masked, findings)
		}
	}
	require.NoError(t, rows.Err())
	scan.print(t.Output())
}

var scanPlaceholderRe = regexp.MustCompile(`\[SECRET:[a-z0-9_]+:[0-9a-f]{14}\]`)

const (
	scanSampleSize = 20
	scanContext    = 40
)

type scanReport struct {
	toolResults int
	refusals    int
	counts      map[string]int
	seen        map[string]int
	samples     map[string][]string
	rng         *rand.Rand
}

func newScanReport() *scanReport {
	return &scanReport{
		counts:  map[string]int{},
		seen:    map[string]int{},
		samples: map[string][]string{},
		rng:     rand.New(rand.NewPCG(1, 2)),
	}
}

// scanPart is one text of a message that reaches a model, and whether the
// model request masks it as JSON.
type scanPart struct {
	text string
	json bool
}

// scanTexts returns the texts of a message's parts that reach a model:
// prompts and replies, tool call inputs and tool results.
func scanTexts(t *testing.T, raw string, scan *scanReport) []scanPart {
	var parts []struct {
		Type string `json:"type"`
		Data struct {
			Text    string `json:"text"`
			Input   string `json:"input"`
			Content string `json:"content"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &parts))
	var texts []scanPart
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, scanPart{text: p.Data.Text})
		case "tool_call":
			texts = append(texts, scanPart{text: p.Data.Input, json: true})
		case "tool_result":
			scan.toolResults++
			if strings.HasPrefix(p.Data.Content, secrets.GuardRefusal) {
				scan.refusals++
			}
			texts = append(texts, scanPart{text: p.Data.Content, json: secrets.IsJSON(p.Data.Content)})
		}
	}
	return texts
}

// add records the findings in one masked text and keeps a uniform random
// sample of excerpts per rule (reservoir sampling). Placeholders already in
// the original text (quoted from an earlier scan, say) aren't findings.
func (s *scanReport) add(original, masked string, findings []secrets.Finding) {
	rules := map[string]string{}
	for _, f := range findings {
		s.counts[f.Rule+" / "+f.Kind]++
		rules[f.Placeholder] = f.Rule
	}
	for _, loc := range scanPlaceholderRe.FindAllStringIndex(masked, -1) {
		placeholder := masked[loc[0]:loc[1]]
		rule, ok := rules[placeholder]
		if !ok || strings.Contains(original, placeholder) {
			continue
		}
		s.seen[rule]++
		excerpt := strings.Join(strings.Fields(masked[max(0, loc[0]-scanContext):min(len(masked), loc[1]+scanContext)]), " ")
		if len(s.samples[rule]) < scanSampleSize {
			s.samples[rule] = append(s.samples[rule], excerpt)
		} else if i := s.rng.IntN(s.seen[rule]); i < scanSampleSize {
			s.samples[rule][i] = excerpt
		}
	}
}

func (s *scanReport) print(w io.Writer) {
	fmt.Fprintf(w, "Tool results scanned: %d; guard refusals: %d\n\n", s.toolResults, s.refusals)
	fmt.Fprintln(w, "Findings by rule / kind (per 1,000 tool results):")
	for _, key := range slices.Sorted(maps.Keys(s.counts)) {
		fmt.Fprintf(w, "  %-40s %6d  (%.1f)\n", key, s.counts[key], 1000*float64(s.counts[key])/float64(max(1, s.toolResults)))
	}
	for _, rule := range slices.Sorted(maps.Keys(s.samples)) {
		fmt.Fprintf(w, "\nSample of rule %s (%d of %d):\n", rule, len(s.samples[rule]), s.seen[rule])
		for _, excerpt := range s.samples[rule] {
			fmt.Fprintf(w, "  %s\n", excerpt)
		}
	}
}

func copyIfExists(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	defer dst.Close()
	_, err = io.Copy(dst, src)
	require.NoError(t, err)
}

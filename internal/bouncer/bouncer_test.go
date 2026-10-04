package bouncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/systemone"
	"github.com/stretchr/testify/require"
)

func batteryBody(hazard float64, severity float64) string {
	var parts []string
	for _, q := range HazardQuestions {
		v := 0.05
		if q == QDestructive {
			v = hazard
		}
		parts = append(parts, fmt.Sprintf(`%q:{"type":"noul","noul":%v}`, q, v))
	}
	parts = append(parts, fmt.Sprintf(`%q:{"type":"score","score":%v}`, QSeverity, severity))
	return `{"model":"von-1.0.0","answers":{` + strings.Join(parts, ",") + `},"usage":{"input_tokens":120,"output_tokens":14}}`
}

const testAPIKey = "test-secret-key-0123456789"

func newTestClient(url string) *systemone.Client {
	return &systemone.Client{
		URL:        url,
		APIKey:     testAPIKey,
		AuthScheme: "Api-Key",
		Model:      "von-1.0.0",
		HTTP:       &http.Client{},
		Backoff:    []time.Duration{time.Millisecond},
	}
}

func newTestBouncer(url string) *Bouncer {
	return New(newTestClient(url), DefaultThresholds(), false)
}

var eligible = permission.AssessInput{ToolName: "bash", Action: "execute", Input: "go test ./..."}

func decodeRecord(t *testing.T, a permission.Assessment) permission.AssessmentRecord {
	t.Helper()
	var rec permission.AssessmentRecord
	require.NoError(t, json.Unmarshal(a.Details, &rec))
	require.Equal(t, permission.AssessmentSchemaVersion, rec.SchemaVersion)
	require.Equal(t, BatteryVersion, rec.BatteryVersion)
	require.Equal(t, DefaultThresholds().asMap(), rec.Thresholds)
	require.Empty(t, rec.Mode)
	return rec
}

func TestBouncerRoutesCannedAnswers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hazard   float64
		severity float64
		want     permission.AssessOutcome
		outcome  string
	}{
		{"allow", 0.05, 0, permission.AssessAllow, "allow"},
		{"escalate", 0.4, 1.6, permission.AssessEscalate, "escalate"},
		{"mid hazard cheap to undo", 0.4, 1, permission.AssessAllow, "allow"},
		{"deny", 0.97, 3, permission.AssessDeny, "deny"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, batteryBody(tt.hazard, tt.severity))
			}))
			defer srv.Close()

			got, err := newTestBouncer(srv.URL).Assess(t.Context(), eligible)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Outcome)
			require.NotEmpty(t, got.Reason)

			rec := decodeRecord(t, got)
			require.Equal(t, tt.outcome, rec.Outcome)
			require.Equal(t, "von-1.0.0", rec.Model)
			require.InDelta(t, tt.hazard, rec.Nouls[QDestructive], 1e-9)
			require.Len(t, rec.Nouls, len(HazardQuestions))
			require.NotNil(t, rec.Severity)
			require.InDelta(t, tt.severity, *rec.Severity, 1e-9)
			require.Equal(t, 120, rec.InputTokens)
			require.Equal(t, 14, rec.OutputTokens)
		})
	}
}

func TestBouncerDetailsJSONShape(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, batteryBody(0.05, 0))
	}))
	defer srv.Close()

	got, err := newTestBouncer(srv.URL).Assess(t.Context(), eligible)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(got.Details, &raw))
	require.EqualValues(t, 1, raw["schema_version"])
	require.Equal(t, BatteryVersion, raw["battery_version"])
}

func TestBouncerRecordsTriggers(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, batteryBody(0.4, 1.6))
	}))
	defer srv.Close()

	got, err := newTestBouncer(srv.URL).Assess(t.Context(), eligible)
	require.NoError(t, err)
	rec := decodeRecord(t, got)
	require.NotEmpty(t, rec.Triggers)
	for axis, effect := range rec.Triggers {
		require.Contains(t, append(slices.Clone(HazardQuestions), QSeverity), axis)
		require.Equal(t, permission.TriggerEscalate, effect)
	}
}

func TestBouncerServerErrorIsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	got, err := newTestBouncer(srv.URL).Assess(t.Context(), eligible)
	require.Error(t, err)
	require.Equal(t, permission.AssessEscalate, got.Outcome)
	rec := decodeRecord(t, got)
	require.Equal(t, "error", rec.Outcome)
	require.NotEmpty(t, rec.Error)
	require.NotContains(t, rec.Error, testAPIKey)
}

func TestBouncerBreaker(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, batteryBody(0.05, 0))
	}))
	defer srv.Close()

	a := newTestBouncer(srv.URL)
	a.Client.Backoff = nil
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	a.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}

	for range breakerThreshold {
		_, err := a.Assess(t.Context(), eligible)
		require.Error(t, err)
	}
	require.Equal(t, int32(breakerThreshold), hits.Load())

	got, err := a.Assess(t.Context(), eligible)
	require.NoError(t, err)
	require.Equal(t, permission.AssessEscalate, got.Outcome)
	rec := decodeRecord(t, got)
	require.Equal(t, "skipped", rec.Outcome)
	require.Equal(t, "bouncer unavailable", rec.SkipReason)
	require.Equal(t, int32(breakerThreshold), hits.Load())

	mu.Lock()
	now = now.Add(61 * time.Second)
	mu.Unlock()
	healthy.Store(true)

	got, err = a.Assess(t.Context(), eligible)
	require.NoError(t, err)
	require.Equal(t, permission.AssessAllow, got.Outcome)
	require.Equal(t, int32(breakerThreshold+1), hits.Load())
}

func TestBouncerCallerCancelDoesNotTripBreaker(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := newTestBouncer(srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range breakerThreshold + 1 {
		_, err := a.Assess(ctx, eligible)
		require.ErrorIs(t, err, context.Canceled)
	}
	allowed, probe := a.breaker.allow(a.now())
	require.True(t, allowed)
	require.False(t, probe)
}

func TestBouncerIneligibleMakesNoRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected HTTP request")
	}))
	defer srv.Close()

	got, err := newTestBouncer(srv.URL).Assess(t.Context(), permission.AssessInput{ToolName: "lsp_rename"})
	require.NoError(t, err)
	require.Equal(t, permission.AssessEscalate, got.Outcome)
	rec := decodeRecord(t, got)
	require.Equal(t, "skipped", rec.Outcome)
	require.Equal(t, skipNotEligible, rec.SkipReason)
}

func TestBouncerConcurrencyCap(t *testing.T) {
	t.Parallel()

	const calls = 6
	release := make(chan struct{})
	var inFlight, maxInFlight atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		_, _ = io.WriteString(w, batteryBody(0.05, 0))
	}))
	defer srv.Close()

	a := newTestBouncer(srv.URL)
	var wg sync.WaitGroup
	results := make(chan permission.AssessOutcome, calls)
	for range calls {
		wg.Go(func() {
			got, err := a.Assess(t.Context(), eligible)
			if err != nil {
				t.Errorf("assess: %v", err)
			}
			results <- got.Outcome
		})
	}

	require.Eventually(t, func() bool { return inFlight.Load() == maxConcurrent }, 5*time.Second, time.Millisecond)
	require.Never(t, func() bool { return inFlight.Load() > maxConcurrent }, 50*time.Millisecond, time.Millisecond)
	require.Len(t, a.sem, maxConcurrent)

	close(release)
	wg.Wait()
	close(results)
	for o := range results {
		require.Equal(t, permission.AssessAllow, o)
	}
	require.Equal(t, int32(maxConcurrent), maxInFlight.Load())
}

func TestBouncerBusyWhenContextDone(t *testing.T) {
	t.Parallel()

	a := newTestBouncer("http://127.0.0.1:0")
	for range maxConcurrent {
		a.sem <- struct{}{}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := a.Assess(ctx, eligible)
	require.NoError(t, err)
	rec := decodeRecord(t, got)
	require.Equal(t, "skipped", rec.Outcome)
	require.Equal(t, skipBusy, rec.SkipReason)
}

func TestBouncerBreakerHalfOpenSingleProbe(t *testing.T) {
	t.Parallel()

	var hits, probeHits atomic.Int32
	var probing atomic.Bool
	probeEntered := make(chan struct{})
	probeRelease := make(chan bool) // true: succeed, false: fail.
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if probing.Load() && probeHits.Add(1) == 1 {
			probeEntered <- struct{}{}
			select {
			case ok := <-probeRelease:
				if !ok {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			case <-stop:
				return
			}
		} else if !probing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, batteryBody(0.05, 0))
	}))
	defer srv.Close()
	defer close(stop)

	a := newTestBouncer(srv.URL)
	a.Client.Backoff = nil
	var mu sync.Mutex
	start := time.Unix(1_700_000_000, 0)
	now := start
	setNow := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = start.Add(d)
	}
	a.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	requireSkipped := func() {
		t.Helper()
		before := hits.Load()
		got, err := a.Assess(t.Context(), eligible)
		require.NoError(t, err)
		rec := decodeRecord(t, got)
		require.Equal(t, "skipped", rec.Outcome)
		require.Equal(t, skipUnavailable, rec.SkipReason)
		require.Equal(t, before, hits.Load())
	}
	probe := func(succeed bool, failAt time.Duration) (permission.Assessment, error) {
		t.Helper()
		probeHits.Store(0)
		type result struct {
			a   permission.Assessment
			err error
		}
		done := make(chan result, 1)
		go func() {
			got, err := a.Assess(t.Context(), eligible)
			done <- result{got, err}
		}()
		<-probeEntered
		// Other callers are turned away while the probe is in flight.
		requireSkipped()
		requireSkipped()
		setNow(failAt)
		probeRelease <- succeed
		r := <-done
		return r.a, r.err
	}

	for range breakerThreshold {
		_, err := a.Assess(t.Context(), eligible)
		require.Error(t, err)
	}
	requireSkipped()

	probing.Store(true)
	setNow(61 * time.Second)
	_, err := probe(false, 100*time.Second)
	require.Error(t, err)

	// The failed probe reopens the breaker for a full cooldown from the
	// failure, not from when the probe started.
	setNow(150 * time.Second)
	requireSkipped()

	setNow(161 * time.Second)
	got, err := probe(true, 161*time.Second)
	require.NoError(t, err)
	require.Equal(t, permission.AssessAllow, got.Outcome)

	// Closed: calls go through concurrently again.
	before := hits.Load()
	got, err = a.Assess(t.Context(), eligible)
	require.NoError(t, err)
	require.Equal(t, permission.AssessAllow, got.Outcome)
	require.Equal(t, before+1, hits.Load())
}

func TestWarmBypassesBreakerAndNeverRetries(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"model":"von-1.0.0","answers":{"warm":{"type":"noul","noul":0.9}},"usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	defer srv.Close()

	a := newTestBouncer(srv.URL)
	a.Client.Backoff = []time.Duration{time.Millisecond, time.Millisecond}

	require.Error(t, a.Warm(t.Context()))
	require.Equal(t, int32(1), hits.Load(), "warm-up must not retry")
	allowed, _ := a.breaker.allow(a.now())
	require.True(t, allowed, "warm-up failure must not trip the breaker")
	require.Len(t, a.Client.Backoff, 2, "warm-up must not mutate the shared client")

	for range breakerThreshold {
		a.breaker.fail(a.now(), false)
	}
	healthy.Store(true)
	require.NoError(t, a.Warm(t.Context()), "warm-up runs even with the breaker open")
	require.Equal(t, int32(2), hits.Load())
}

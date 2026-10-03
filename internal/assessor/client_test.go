package assessor

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testAPIKey = "test-secret-key-0123456789"

func testQuestions() map[string]Question {
	return map[string]Question{
		"danger": {Type: "noul", Instructions: "Is it dangerous?"},
		"level":  {Type: "score", Instructions: "How bad?", Criteria: []string{"none", "some", "lots"}},
		"kind":   {Type: "choice", Instructions: "What kind?", Criteria: map[string]string{"read": "Reads.", "write": "Writes."}},
	}
}

const validBody = `{"model":"von-1.0.0","answers":{
	"danger":{"type":"noul","noul":0.25},
	"level":{"type":"score","score":1},
	"kind":{"type":"choice","choice":"read","probabilities":{"read":0.8,"write":0.2},"confidence":0.6}},
	"usage":{"input_tokens":46,"output_tokens":16}}`

func newTestClient(url string) *Client {
	return &Client{
		URL:        url,
		APIKey:     testAPIKey,
		AuthScheme: "Api-Key",
		Model:      "von-1.0.0",
		HTTP:       &http.Client{},
		Backoff:    []time.Duration{time.Millisecond},
	}
}

func TestClientSendsAuthAndBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Api-Key "+testAPIKey, r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.JSONEq(t, `"von-1.0.0"`, string(body["model"]))
		require.JSONEq(t, `{"tool":"bash"}`, string(body["state"]))

		var qs map[string]Question
		require.NoError(t, json.Unmarshal(body["questions"], &qs))
		require.Len(t, qs, 3)
		require.Equal(t, "noul", qs["danger"].Type)

		_, _ = io.WriteString(w, validBody)
	}))
	defer srv.Close()

	resp, err := newTestClient(srv.URL).Evaluate(t.Context(), map[string]any{"tool": "bash"}, testQuestions())
	require.NoError(t, err)
	require.Equal(t, "von-1.0.0", resp.Model)
	require.InDelta(t, 0.25, *resp.Answers["danger"].Noul, 1e-9)
	require.InDelta(t, 1.0, *resp.Answers["level"].Score, 1e-9)
	require.Equal(t, "read", resp.Answers["kind"].Choice)
	require.Equal(t, Usage{InputTokens: 46, OutputTokens: 16}, resp.Usage)
}

func TestClientRetriesOn429(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, validBody)
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).Evaluate(t.Context(), map[string]any{}, testQuestions())
	require.NoError(t, err)
	require.Equal(t, int32(2), hits.Load())
}

func TestClientDoesNotRetryOn400(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad request")
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).Evaluate(t.Context(), map[string]any{}, testQuestions())
	require.Error(t, err)
	require.Contains(t, err.Error(), "400")
	require.NotErrorIs(t, err, ErrRetryable)
	require.Equal(t, int32(1), hits.Load())
}

func TestClientErrorOmitsAPIKey(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized")
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).Evaluate(t.Context(), map[string]any{}, testQuestions())
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
	require.NotContains(t, err.Error(), testAPIKey)
}

func TestClientCancelDuringBackoff(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.Backoff = []time.Duration{time.Hour}
	_, err := c.Evaluate(ctx, map[string]any{}, testQuestions())
	require.ErrorIs(t, err, context.Canceled)
}

func TestClientOverLimitBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", maxResponseBytes+1))
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).Evaluate(t.Context(), map[string]any{}, testQuestions())
	require.ErrorContains(t, err, "exceeds")
}

func TestClientConnectionResetMidBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, buf, err := hj.Hijack()
		require.NoError(t, err)
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"model\":")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).Evaluate(t.Context(), map[string]any{}, testQuestions())
	require.ErrorContains(t, err, "read assessor response")
}

func TestClientRejectsInvalidAnswers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"missing answer", `{"model":"m","answers":{"danger":{"type":"noul","noul":0.1},"level":{"type":"score","score":1}}}`},
		{"wrong type", `{"model":"m","answers":{"danger":{"type":"score","score":0.1},"level":{"type":"score","score":1},"kind":{"type":"choice","choice":"read"}}}`},
		{"noul above one", `{"model":"m","answers":{"danger":{"type":"noul","noul":1.5},"level":{"type":"score","score":1},"kind":{"type":"choice","choice":"read"}}}`},
		{"noul below zero", `{"model":"m","answers":{"danger":{"type":"noul","noul":-0.1},"level":{"type":"score","score":1},"kind":{"type":"choice","choice":"read"}}}`},
		{"score out of range", `{"model":"m","answers":{"danger":{"type":"noul","noul":0.1},"level":{"type":"score","score":7},"kind":{"type":"choice","choice":"read"}}}`},
		{"infinite noul", `{"model":"m","answers":{"danger":{"type":"noul","noul":1e999},"level":{"type":"score","score":1},"kind":{"type":"choice","choice":"read"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			_, err := newTestClient(srv.URL).Evaluate(t.Context(), map[string]any{}, testQuestions())
			require.Error(t, err)
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	f := func(v float64) *float64 { return &v }
	valid := func() Response {
		return Response{Model: "m", Answers: map[string]Answer{
			"danger": {Type: "noul", Noul: f(0.2)},
			"level":  {Type: "score", Score: f(2)},
			"kind":   {Type: "choice", Choice: "write"},
		}}
	}

	require.NoError(t, Validate(valid(), testQuestions()))

	tests := []struct {
		name   string
		mutate func(r *Response)
	}{
		{"empty model", func(r *Response) { r.Model = "" }},
		{"missing answer", func(r *Response) { delete(r.Answers, "kind") }},
		{"wrong type", func(r *Response) { r.Answers["danger"] = Answer{Type: "choice", Choice: "read"} }},
		{"nil noul", func(r *Response) { r.Answers["danger"] = Answer{Type: "noul"} }},
		{"NaN noul", func(r *Response) { r.Answers["danger"] = Answer{Type: "noul", Noul: f(math.NaN())} }},
		{"Inf noul", func(r *Response) { r.Answers["danger"] = Answer{Type: "noul", Noul: f(math.Inf(1))} }},
		{"noul 1.5", func(r *Response) { r.Answers["danger"] = Answer{Type: "noul", Noul: f(1.5)} }},
		{"noul -0.1", func(r *Response) { r.Answers["danger"] = Answer{Type: "noul", Noul: f(-0.1)} }},
		{"nil score", func(r *Response) { r.Answers["level"] = Answer{Type: "score"} }},
		{"score 7", func(r *Response) { r.Answers["level"] = Answer{Type: "score", Score: f(7)} }},
		{"score negative", func(r *Response) { r.Answers["level"] = Answer{Type: "score", Score: f(-1)} }},
		{"NaN score", func(r *Response) { r.Answers["level"] = Answer{Type: "score", Score: f(math.NaN())} }},
		{"unknown choice", func(r *Response) { r.Answers["kind"] = Answer{Type: "choice", Choice: "delete"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := valid()
			tt.mutate(&r)
			require.Error(t, Validate(r, testQuestions()))
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	require.Equal(t, "héll", truncate("héllo", 4))
	require.Equal(t, "héllo", truncate("héllo", 10))
	require.Empty(t, truncate("héllo", 0))
}

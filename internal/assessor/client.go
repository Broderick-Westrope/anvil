// Package assessor asks a System One classifier whether a tool call
// needs a human.
package assessor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"time"
)

// Question is a System One question (noul, choice, or score).
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer is a System One answer. Only the fields for Type are set.
type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// Usage reports the tokens a System One call consumed.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Response is a decoded and validated System One response.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Client calls a TypeSafe-compatible /v1/systemone endpoint.
type Client struct {
	URL        string // Full endpoint URL.
	APIKey     string
	AuthScheme string // "Api-Key" (Baseten) or "Bearer" (TypeSafe).
	Model      string
	HTTP       *http.Client
	Backoff    []time.Duration // Wait before retry i+1.
}

// ErrRetryable marks a response status that is worth retrying.
var ErrRetryable = errors.New("retryable status")

const maxResponseBytes = 1 << 20

// Evaluate sends state and questions to the endpoint and returns the
// validated response. It retries retryable statuses per Backoff.
func (c *Client) Evaluate(ctx context.Context, state any, questions map[string]Question) (Response, error) {
	body, err := json.Marshal(request{Model: c.Model, State: state, Questions: questions})
	if err != nil {
		return Response{}, fmt.Errorf("marshal assessor request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= len(c.Backoff); attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(c.Backoff[attempt-1])
			select {
			case <-ctx.Done():
				timer.Stop()
				return Response{}, ctx.Err()
			case <-timer.C:
			}
		}
		resp, err := c.do(ctx, body)
		if err == nil {
			if err := Validate(resp, questions); err != nil {
				return Response{}, fmt.Errorf("invalid assessor response: %w", err)
			}
			return resp, nil
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		lastErr = err
		if !errors.Is(err, ErrRetryable) {
			return Response{}, err
		}
	}
	return Response{}, lastErr
}

func (c *Client) do(ctx context.Context, body []byte) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build assessor request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.AuthScheme+" "+c.APIKey)

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("assessor request failed: %w", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return Response{}, fmt.Errorf("read assessor response: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return Response{}, fmt.Errorf("assessor response exceeds %d bytes", maxResponseBytes)
	}

	if res.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("assessor returned status %d: %s", res.StatusCode, truncate(string(raw), 300))
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode == 529 || res.StatusCode >= 500 {
			return Response{}, fmt.Errorf("%w: %w", ErrRetryable, statusErr)
		}
		return Response{}, statusErr
	}

	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, fmt.Errorf("decode assessor response: %w", err)
	}
	return out, nil
}

// Validate checks that resp answers exactly the given questions, each with
// the right type and an in-range value.
func Validate(resp Response, questions map[string]Question) error {
	if resp.Model == "" {
		return errors.New("response has no model")
	}
	for id := range resp.Answers {
		if _, ok := questions[id]; !ok {
			return fmt.Errorf("unexpected answer %q", id)
		}
	}
	for id, q := range questions {
		a, ok := resp.Answers[id]
		if !ok {
			return fmt.Errorf("missing answer for %q", id)
		}
		if a.Type != q.Type {
			return fmt.Errorf("answer %q has type %q, want %q", id, a.Type, q.Type)
		}
		switch q.Type {
		case "noul":
			if a.Noul == nil || !finite(*a.Noul) || *a.Noul < 0 || *a.Noul > 1 {
				return fmt.Errorf("answer %q has invalid noul", id)
			}
		case "score":
			levels := criteriaLen(q.Criteria)
			if a.Score == nil || !finite(*a.Score) || *a.Score < 0 || *a.Score > float64(levels-1) {
				return fmt.Errorf("answer %q has invalid score", id)
			}
		case "choice":
			if !slices.Contains(criteriaKeys(q.Criteria), a.Choice) {
				return fmt.Errorf("answer %q has unknown choice %q", id, a.Choice)
			}
		default:
			return fmt.Errorf("question %q has unknown type %q", id, q.Type)
		}
	}
	return nil
}

func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

func criteriaLen(c any) int {
	switch v := c.(type) {
	case []string:
		return len(v)
	case []any:
		return len(v)
	case map[string]string:
		return len(v)
	case map[string]any:
		return len(v)
	}
	return 0
}

func criteriaKeys(c any) []string {
	var keys []string
	switch v := c.(type) {
	case map[string]string:
		for k := range v {
			keys = append(keys, k)
		}
	case map[string]any:
		for k := range v {
			keys = append(keys, k)
		}
	}
	return keys
}

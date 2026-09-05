package mcpauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
)

// callbackResult holds the authorization code and state returned by
// the OAuth callback.
type callbackResult struct {
	Code  string
	State string
	Err   error
}

// startCallbackServer starts an ephemeral HTTP server that listens
// for the OAuth callback. If addr is empty, it listens on a random
// port on 127.0.0.1. Otherwise it listens on the given addr (e.g.
// "localhost:3118").
func startCallbackServer(ctx context.Context, addr string) (net.Listener, <-chan callbackResult, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"failed to start OAuth callback listener on %s "+
				"(is another Anvil instance authenticating?): %w",
			addr, err)
	}

	ch := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		// Check for OAuth error response (RFC 6749 §4.1.2.1).
		if errCode := r.URL.Query().Get("error"); errCode != "" {
			desc := r.URL.Query().Get("error_description")
			if desc == "" {
				desc = errCode
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w,
				`<!DOCTYPE html><html><body><h1>Authentication failed</h1><p>%s</p></body></html>`,
				desc)
			ch <- callbackResult{
				Err: fmt.Errorf(
					"OAuth authorization failed: %s", desc),
			}
			return
		}

		code := r.URL.Query().Get("code")
		st := r.URL.Query().Get("state")
		if code == "" {
			http.Error(w, "Missing authorization code",
				http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w,
			`<!DOCTYPE html><html><body><h1>Authentication complete</h1>`+
				`<p>You can close this tab and return to the terminal.</p></body></html>`)
		ch <- callbackResult{Code: code, State: st}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	return listener, ch, nil
}

// generateState produces a random 16-byte hex-encoded state string.
func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

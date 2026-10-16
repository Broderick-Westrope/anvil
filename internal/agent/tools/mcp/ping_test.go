package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// noPingSession connects to an in-memory server that answers ping with
// MethodNotFound, like a server that never implemented the method.
func noPingSession(t *testing.T) *ClientSession {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "no-ping"}, nil)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "ping" {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
			}
			return next(ctx, method, req)
		}
	})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	client := mcp.NewClient(&mcp.Implementation{Name: "anvil-test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	return &ClientSession{ClientSession: clientSession, cancel: cancel}
}

func TestGetOrRenewClient_KeepsSessionWhenPingUnsupported(t *testing.T) {
	const name = "test-no-ping"

	sess := noPingSession(t)
	sessions.Set(name, sess)
	t.Cleanup(func() {
		if s, ok := sessions.Take(name); ok {
			_ = s.Close()
		}
		states.Del(name)
	})

	origNewSession := newSession
	newSession = func(context.Context, string, config.MCPConfig, config.VariableResolver, db.Querier) (*ClientSession, error) {
		t.Fatal("a server that answers ping with MethodNotFound must not be rebuilt")
		return nil, nil
	}
	t.Cleanup(func() { newSession = origNewSession })

	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{name: {Type: config.MCPStdio}}})
	for range 3 {
		got, err := getOrRenewClient(context.Background(), cfg, name)
		require.NoError(t, err)
		require.Same(t, sess, got)
	}
}

func TestPingSession_ClosedSessionStillFails(t *testing.T) {
	sess := noPingSession(t)
	require.NoError(t, sess.Close())

	require.Error(t, pingSession(context.Background(), sess, time.Second))
}

// TestPingSession_NewProtocol pins pings on sessions that negotiated the
// stateless 2026-07-28 protocol. Servers reject any call there that lacks the
// SEP-2575 _meta fields, and go-sdk v1.8.0's Ping doesn't add them, so a bare
// ping failed and every tool call rebuilt a healthy session.
func TestPingSession_NewProtocol(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "new-protocol"}, nil)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	client := mcp.NewClient(&mcp.Implementation{Name: "anvil-test"}, nil)
	clientSession, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	require.NoError(t, err)
	sess := &ClientSession{ClientSession: clientSession, cancel: cancel}
	t.Cleanup(func() { _ = sess.Close() })

	require.GreaterOrEqual(t, sess.InitializeResult().ProtocolVersion, "2026-07-28",
		"the test must exercise the new protocol")
	require.NoError(t, pingSession(context.Background(), sess, time.Second))
}

func TestPingParams(t *testing.T) {
	t.Run("stateless protocol carries the SEP-2575 meta", func(t *testing.T) {
		sess := noPingSession(t)
		t.Cleanup(func() { _ = sess.Close() })

		params := pingParams(sess)

		require.NotNil(t, params)
		require.Equal(t, sess.InitializeResult().ProtocolVersion, params.Meta[mcp.MetaKeyProtocolVersion])
		require.Equal(t, map[string]any{}, params.Meta[mcp.MetaKeyClientCapabilities])
	})

	t.Run("older protocol sends a bare ping", func(t *testing.T) {
		serverTransport, clientTransport := mcp.NewInMemoryTransports()
		server := mcp.NewServer(&mcp.Implementation{Name: "old-protocol"}, nil)
		serverSession, err := server.Connect(context.Background(), serverTransport, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = serverSession.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "anvil-test"}, nil).Connect(ctx, clientTransport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
		require.NoError(t, err)
		sess := &ClientSession{ClientSession: clientSession, cancel: cancel}
		t.Cleanup(func() { _ = sess.Close() })
		require.Equal(t, "2025-06-18", sess.InitializeResult().ProtocolVersion)

		require.Nil(t, pingParams(sess))
	})
}

func TestStdioCheck_CommandWithoutArgs(t *testing.T) {
	t.Parallel()

	err := stdioCheck(&exec.Cmd{Path: filepath.Join(t.TempDir(), "missing")})

	require.Error(t, err)
}

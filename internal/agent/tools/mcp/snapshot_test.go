package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func pingHTTPServer(t *testing.T) string {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "ping"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "ping", Description: "Replies pong."},
		func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil, nil
		})
	ts := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestInitializeRecordsTheStartupSnapshot(t *testing.T) {
	const (
		connected = "snap-connected"
		lazy      = "snap-lazy"
		disabled  = "snap-disabled"
	)
	url := pingHTTPServer(t)
	t.Cleanup(func() {
		for _, name := range []string{connected, lazy, disabled} {
			if s, ok := sessions.Take(name); ok {
				_ = s.Close()
			}
			states.Del(name)
			allTools.Del(name)
		}
		ResetInitForTest()
	})
	ResetInitForTest()
	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{
		connected: {Type: config.MCPHttp, URL: url, Route: config.MCPRouteDirect},
		lazy:      {Type: config.MCPStdio, Command: "echo", LazyDescription: "Lazy."},
		disabled:  {Type: config.MCPStdio, Command: "echo", Disabled: true},
	}})
	ArmInit()
	_, ready := StartupSnapshot(cfg)
	require.False(t, ready, "no snapshot while initialisation is running")

	Initialize(context.Background(), nil, cfg, nil)

	// A config write after startup must not reach the snapshot.
	cfg.Config().MCP[lazy] = config.MCPConfig{Type: config.MCPStdio, Command: "echo"}
	delete(cfg.Config().MCP, connected)

	snap, ready := StartupSnapshot(cfg)
	require.True(t, ready)
	require.Equal(t, StateConnected, snap[connected].State)
	require.Equal(t, config.MCPRouteDirect, snap[connected].Config.Route)
	require.Equal(t, SentToolBytes(connected), snap[connected].SentBytes)
	require.Positive(t, snap[connected].SentBytes)
	require.Equal(t, StateDeferred, snap[lazy].State)
	require.Equal(t, "Lazy.", snap[lazy].Config.LazyDescription)
	require.Equal(t, StateDisabled, snap[disabled].State)
}

func TestStartupSnapshotWithoutArmingReadsTheCurrentState(t *testing.T) {
	const name = "snap-unarmed"
	t.Cleanup(func() {
		states.Del(name)
		allTools.Del(name)
		ResetInitForTest()
	})
	ResetInitForTest()
	SetToolsForTest(name, &Tool{Name: "ping"})
	SetStateForTest(name, StateConnected)
	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{name: {Type: config.MCPStdio, Command: "x"}}})

	snap, ready := StartupSnapshot(cfg)
	require.True(t, ready)
	require.Equal(t, StateConnected, snap[name].State)
	require.Equal(t, SentToolBytes(name), snap[name].SentBytes)
}

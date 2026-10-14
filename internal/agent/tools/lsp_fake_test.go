package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"
)

// fakeLSPEnv carries a fake LSP server's canned replies, keyed by method,
// to the test binary re-run as that server.
const fakeLSPEnv = "ANVIL_TOOLS_FAKE_LSP"

// fakeLSPFileType is the only file type the fake server handles, so no
// real language server ever claims the test files.
const fakeLSPFileType = "fakelang"

// fakeLSPReply is a canned reply to one method: a result, or an error
// with the given message.
type fakeLSPReply struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// TestMain drops the "Error reading stderr" record powernap logs when a
// fake language server shuts down. Its stderr reader races the process
// wait that closes the pipe, so the record is noise, not a failure.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(dropClosedStderrHandler{slog.NewTextHandler(os.Stderr, nil)}))
	os.Exit(m.Run())
}

// dropClosedStderrHandler passes every record on except powernap's
// "Error reading stderr" for a pipe that is already closed.
type dropClosedStderrHandler struct{ slog.Handler }

func (h dropClosedStderrHandler) Handle(ctx context.Context, r slog.Record) error {
	closed := false
	if r.Message == "Error reading stderr" {
		r.Attrs(func(a slog.Attr) bool {
			err, ok := a.Value.Any().(error)
			closed = ok && errors.Is(err, os.ErrClosed)
			return !closed
		})
	}
	if closed {
		return nil
	}
	return h.Handler.Handle(ctx, r)
}

func (h dropClosedStderrHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return dropClosedStderrHandler{h.Handler.WithAttrs(attrs)}
}

func (h dropClosedStderrHandler) WithGroup(name string) slog.Handler {
	return dropClosedStderrHandler{h.Handler.WithGroup(name)}
}

// TestFakeLSPServer is not a test: it is the fake language server that
// newFakeLSP starts by re-running the test binary.
func TestFakeLSPServer(t *testing.T) {
	replies := os.Getenv(fakeLSPEnv)
	if replies == "" {
		t.Skip("only runs as the fake LSP server process")
	}
	var canned map[string]json.RawMessage
	err := json.Unmarshal([]byte(replies), &canned)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	serveFakeLSP(os.Stdin, os.Stdout, canned)
	os.Exit(0)
}

func serveFakeLSP(in io.Reader, out io.Writer, canned map[string]json.RawMessage) {
	reader := textproto.NewReader(bufio.NewReader(in))
	for {
		header, err := reader.ReadMIMEHeader()
		if err != nil {
			return
		}
		length, err := strconv.Atoi(header.Get("Content-Length"))
		if err != nil {
			return
		}
		body := make([]byte, length)
		_, err = io.ReadFull(reader.R, body)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		err = json.Unmarshal(body, &msg)
		if err != nil {
			return
		}
		if msg.Method == "exit" {
			return
		}
		if msg.ID == nil || msg.Method == "" {
			continue
		}
		writeFakeLSPReply(out, msg.ID, fakeLSPResult(msg.Method, canned))
	}
}

func fakeLSPResult(method string, canned map[string]json.RawMessage) map[string]any {
	if raw, ok := canned[method]; ok {
		var reply struct {
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		_ = json.Unmarshal(raw, &reply)
		if reply.Error != "" {
			return map[string]any{"error": map[string]any{"code": -32603, "message": reply.Error}}
		}
		return map[string]any{"result": reply.Result}
	}
	if method == "initialize" {
		return map[string]any{"result": map[string]any{"capabilities": map[string]any{
			"renameProvider":         true,
			"callHierarchyProvider":  true,
			"documentSymbolProvider": true,
		}}}
	}
	return map[string]any{"result": nil}
}

func writeFakeLSPReply(out io.Writer, id json.RawMessage, reply map[string]any) {
	reply["jsonrpc"] = "2.0"
	reply["id"] = id
	body, _ := json.Marshal(reply)
	fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

// newFakeLSP starts a fake language server for workingDir that answers
// each method in replies as given, and returns a manager holding its
// client. Files it handles end in "."+fakeLSPFileType.
func newFakeLSP(t *testing.T, workingDir string, replies map[string]fakeLSPReply) *lsp.Manager {
	t.Helper()
	encoded, err := json.Marshal(replies)
	require.NoError(t, err)
	exe, err := os.Executable()
	require.NoError(t, err)

	cfg := config.LSPConfig{
		Command:   exe,
		Args:      []string{"-test.run=^TestFakeLSPServer$"},
		Env:       map[string]string{fakeLSPEnv: string(encoded)},
		FileTypes: []string{fakeLSPFileType},
	}
	client, err := lsp.New(t.Context(), "fake", cfg, config.IdentityResolver(), workingDir, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	_, err = client.Initialize(t.Context(), workingDir)
	require.NoError(t, err)
	client.SetServerState(lsp.StateReady)

	manager := lsp.NewManager(config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Options:   &config.Options{},
	}))
	manager.Clients().Set("fake", client)
	return manager
}

// writeFakeLangFile writes content to name under dir and returns its
// absolute path.
func writeFakeLangFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	abs, err := filepath.Abs(path)
	require.NoError(t, err)
	return abs
}

// lspShownPath returns path as the LSP tools show it after a round trip
// through a document URI.
func lspShownPath(t *testing.T, path string) string {
	t.Helper()
	shown, err := protocol.DocumentURI(protocol.URIFromPath(path)).Path()
	require.NoError(t, err)
	return shown
}

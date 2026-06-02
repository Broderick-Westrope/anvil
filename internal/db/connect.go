package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/pressly/goose/v3"
)

var (
	pragmas = map[string]string{
		"foreign_keys":  "ON",
		"journal_mode":  "WAL",
		"page_size":     "4096",
		"temp_store":    "MEMORY",
		"cache_size":    "-8000",
		"synchronous":   "NORMAL",
		"secure_delete": "ON",
		"busy_timeout":  "30000",
	}
	gooseInitOnce sync.Once
	gooseInitErr  error
)

//go:embed migrations/*.sql
var FS embed.FS

func init() {
	goose.SetBaseFS(FS)

	if testing.Testing() {
		goose.SetLogger(goose.NopLogger())
	}
}

// connEntry holds a shared database connection and its reference count.
type connEntry struct {
	db       *sql.DB
	refCount int
}

var (
	pool   = make(map[string]*connEntry)
	poolMu sync.Mutex
)

// Connect opens a SQLite database connection for the given data
// directory and runs migrations. If a connection to the same database
// file already exists, the existing connection is returned with its
// reference count incremented. Callers must pair each Connect with a
// [Release] when they no longer need the connection.
func Connect(ctx context.Context, dataDir string) (*sql.DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data.dir is not set")
	}

	dbPath := filepath.Join(dataDir, "anvil.db")
	return connectToPath(ctx, dbPath)
}

// Release decrements the reference count for the database at the given
// data directory. When the count reaches zero the underlying connection
// is closed and removed from the pool.
func Release(dataDir string) error {
	dbPath := filepath.Join(dataDir, "anvil.db")
	return releaseByPath(dbPath)
}

// ConnectGlobal opens a SQLite database connection for the global data
// directory and runs migrations. It behaves like [Connect] but derives
// the database path from [config.GlobalDataDir]. The parent directory
// is created if it does not exist. Callers must pair each ConnectGlobal
// with a [ReleaseGlobal] when they no longer need the connection.
func ConnectGlobal(ctx context.Context) (*sql.DB, error) {
	dir := config.GlobalDataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create global data directory: %w", err)
	}

	dbPath := filepath.Join(dir, "anvil.db")
	return connectToPath(ctx, dbPath)
}

// ReleaseGlobal decrements the reference count for the global database.
// When the count reaches zero the underlying connection is closed and
// removed from the pool.
func ReleaseGlobal() error {
	dbPath := filepath.Join(config.GlobalDataDir(), "anvil.db")
	return releaseByPath(dbPath)
}

// connectToPath is the shared implementation for opening a pooled
// SQLite connection at the given database file path.
func connectToPath(ctx context.Context, dbPath string) (*sql.DB, error) {
	// Resolve to an absolute path so that different relative paths to
	// the same file share a single connection.
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		absPath = dbPath
	}

	poolMu.Lock()
	defer poolMu.Unlock()

	if entry, ok := pool[absPath]; ok {
		entry.refCount++
		return entry.db, nil
	}

	conn, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}

	// Serialize all access through a single connection. SQLite
	// serializes writes at the file level anyway, and allowing multiple
	// pool connections to interleave writes/checkpoints (especially
	// under concurrent sub-agents) has caused WAL/header desync
	// resulting in SQLITE_NOTADB (26) on the next open.
	conn.SetMaxOpenConns(1)

	if err = conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := initGoose(); err != nil {
		conn.Close()
		slog.Error("Failed to initialize goose", "error", err)
		return nil, fmt.Errorf("failed to initialize goose: %w", err)
	}

	if err := goose.Up(conn, "migrations"); err != nil {
		conn.Close()
		slog.Error("Failed to apply migrations", "error", err)
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	pool[absPath] = &connEntry{db: conn, refCount: 1}
	return conn, nil
}

// releaseByPath is the shared implementation for releasing a pooled
// connection identified by its database file path.
func releaseByPath(dbPath string) error {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		absPath = dbPath
	}

	poolMu.Lock()
	defer poolMu.Unlock()

	entry, ok := pool[absPath]
	if !ok {
		return nil
	}

	entry.refCount--
	if entry.refCount > 0 {
		return nil
	}

	delete(pool, absPath)
	return entry.db.Close()
}

// ResetPool closes all pooled connections and clears the pool. This is
// intended for use in tests to ensure a clean state between test cases.
func ResetPool() {
	poolMu.Lock()
	defer poolMu.Unlock()
	for path, entry := range pool {
		entry.db.Close()
		delete(pool, path)
	}
}

func initGoose() error {
	gooseInitOnce.Do(func() {
		goose.SetBaseFS(FS)
		gooseInitErr = goose.SetDialect("sqlite3")
	})

	return gooseInitErr
}

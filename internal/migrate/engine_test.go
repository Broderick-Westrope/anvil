package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/migrate"
	"github.com/Broderick-Westrope/anvil/internal/projects"
	"github.com/stretchr/testify/require"
)

// setupTestDBs creates a global and source database, both fully
// migrated, and returns them along with a cleanup function. The
// source DB path is also returned for use in migration calls.
func setupTestDBs(t *testing.T) (globalDB *sql.DB, sourcePath string, workingDir string) {
	t.Helper()

	globalDir := t.TempDir()
	globalDB, err := db.Connect(context.Background(), globalDir)
	require.NoError(t, err)
	t.Cleanup(func() { db.Release(globalDir) })

	workingDir = t.TempDir()

	// Resolve symlinks so tests can match the path stored after
	// ProjectDB calls filepath.EvalSymlinks (macOS /var →
	// /private/var).
	workingDir, err = filepath.EvalSymlinks(workingDir)
	require.NoError(t, err)

	sourceDir := filepath.Join(workingDir, ".anvil")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	sourceDB, err := db.Connect(context.Background(), sourceDir)
	require.NoError(t, err)
	sourcePath = filepath.Join(sourceDir, "anvil.db")

	// Seed source DB with test data.
	seedSourceDB(t, sourceDB, workingDir)

	// Release source from pool — ProjectDB opens it independently.
	require.NoError(t, db.Release(sourceDir))

	return globalDB, sourcePath, workingDir
}

// seedSourceDB inserts test sessions, messages, files, read_files,
// and OAuth data into the source database.
func seedSourceDB(t *testing.T, database *sql.DB, workingDir string) {
	t.Helper()
	ctx := context.Background()

	// Insert sessions.
	_, err := database.ExecContext(ctx, `
		INSERT INTO sessions (id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at, working_dir)
		VALUES
			('sess-1', 'Session One', 3, 100, 50, 0.01, 1000000, 900000, ?),
			('sess-2', 'Session Two', 1, 200, 100, 0.02, 2000000, 1900000, ?)
	`, workingDir, workingDir)
	require.NoError(t, err)

	// Disable triggers temporarily to seed messages without
	// incrementing message_count.
	_, err = database.ExecContext(ctx, `DROP TRIGGER IF EXISTS update_session_message_count_on_insert`)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `DROP TRIGGER IF EXISTS update_sessions_updated_at`)
	require.NoError(t, err)

	_, err = database.ExecContext(ctx, `
		INSERT INTO messages (id, session_id, role, parts, created_at, updated_at, message_type)
		VALUES
			('msg-1', 'sess-1', 'user', '["hello"]', 900001, 900001, 'message'),
			('msg-2', 'sess-1', 'assistant', '["hi"]', 900002, 900002, 'message'),
			('msg-3', 'sess-1', 'user', '["bye"]', 900003, 900003, 'message'),
			('msg-4', 'sess-2', 'user', '["test"]', 1900001, 1900001, 'message')
	`)
	require.NoError(t, err)

	// Re-create triggers.
	_, err = database.ExecContext(ctx, `
		CREATE TRIGGER IF NOT EXISTS update_session_message_count_on_insert
		AFTER INSERT ON messages
		BEGIN
			UPDATE sessions SET message_count = message_count + 1
			WHERE id = new.session_id;
		END
	`)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `
		CREATE TRIGGER IF NOT EXISTS update_sessions_updated_at
		AFTER UPDATE ON sessions
		BEGIN
			UPDATE sessions SET updated_at = strftime('%s', 'now')
			WHERE id = new.id;
		END
	`)
	require.NoError(t, err)

	// Insert files.
	_, err = database.ExecContext(ctx, `
		INSERT INTO files (id, session_id, path, content, version, created_at, updated_at)
		VALUES
			('file-1', 'sess-1', '/tmp/foo.go', 'package foo', 1, 900001, 900001)
	`)
	require.NoError(t, err)

	// Insert read_files with a relative path and an absolute path.
	_, err = database.ExecContext(ctx, `
		INSERT INTO read_files (session_id, path, read_at)
		VALUES
			('sess-1', 'src/main.go', 900001),
			('sess-1', '/absolute/path.go', 900002)
	`)
	require.NoError(t, err)

	// Insert OAuth tokens.
	_, err = database.ExecContext(ctx, `
		INSERT INTO mcp_oauth_tokens
			(server_name, server_url, access_token, token_type, client_id, created_at, updated_at)
		VALUES
			('github-mcp', 'https://github.com', 'token-abc', 'Bearer', 'client-1', 100, 200)
	`)
	require.NoError(t, err)

	// Insert OAuth clients.
	_, err = database.ExecContext(ctx, `
		INSERT INTO mcp_oauth_clients
			(server_name, server_url, client_id, created_at)
		VALUES
			('github-mcp', 'https://github.com', 'client-1', 100)
	`)
	require.NoError(t, err)
}

func TestMigrateSynchronous(t *testing.T) {
	t.Parallel()

	globalDB, sourcePath, workingDir := setupTestDBs(t)
	ctx := context.Background()

	err := migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	// Verify sessions were copied with correct working_dir.
	var count int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE working_dir = ?`, workingDir).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Verify message_count is preserved (not doubled by triggers).
	var msgCount int64
	err = globalDB.QueryRowContext(ctx,
		`SELECT message_count FROM sessions WHERE id = 'sess-1'`).Scan(&msgCount)
	require.NoError(t, err)
	require.Equal(t, int64(3), msgCount)

	// Verify updated_at is preserved (not clobbered by trigger).
	var updatedAt int64
	err = globalDB.QueryRowContext(ctx,
		`SELECT updated_at FROM sessions WHERE id = 'sess-1'`).Scan(&updatedAt)
	require.NoError(t, err)
	require.Equal(t, int64(1000000), updatedAt)

	// Verify messages were copied.
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 4, count)

	// Verify files were copied.
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM files`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Verify triggers were recreated by inserting a new message.
	_, err = globalDB.ExecContext(ctx, `
		INSERT INTO messages (id, session_id, role, parts, created_at, updated_at, message_type)
		VALUES ('msg-new', 'sess-1', 'user', '["new"]', 999999, 999999, 'message')
	`)
	require.NoError(t, err)

	// The trigger should have incremented message_count.
	err = globalDB.QueryRowContext(ctx,
		`SELECT message_count FROM sessions WHERE id = 'sess-1'`).Scan(&msgCount)
	require.NoError(t, err)
	require.Equal(t, int64(4), msgCount)
}

func TestMigrateBatched(t *testing.T) {
	t.Parallel()

	globalDB, sourcePath, workingDir := setupTestDBs(t)
	ctx := context.Background()

	err := migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 100)
	require.NoError(t, err)

	// Verify message_count matches source (overwritten after trigger
	// artifacts).
	var msgCount int64
	err = globalDB.QueryRowContext(ctx,
		`SELECT message_count FROM sessions WHERE id = 'sess-1'`).Scan(&msgCount)
	require.NoError(t, err)
	require.Equal(t, int64(3), msgCount)

	// Verify updated_at matches source value.
	var updatedAt int64
	err = globalDB.QueryRowContext(ctx,
		`SELECT updated_at FROM sessions WHERE id = 'sess-1'`).Scan(&updatedAt)
	require.NoError(t, err)
	require.Equal(t, int64(1000000), updatedAt)

	// Verify sess-2 as well.
	err = globalDB.QueryRowContext(ctx,
		`SELECT message_count FROM sessions WHERE id = 'sess-2'`).Scan(&msgCount)
	require.NoError(t, err)
	require.Equal(t, int64(1), msgCount)
}

func TestMigrateInsertOrIgnoreSkipsDuplicates(t *testing.T) {
	t.Parallel()

	globalDB, sourcePath, workingDir := setupTestDBs(t)
	ctx := context.Background()

	// Run migration twice — second run should not error.
	err := migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	err = migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	// Verify no duplicate sessions.
	var count int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestMigrateOAuthNewestWins(t *testing.T) {
	t.Parallel()

	globalDir := t.TempDir()

	ctx := context.Background()
	globalDB, err := db.Connect(ctx, globalDir)
	require.NoError(t, err)
	t.Cleanup(func() { db.Release(globalDir) })

	// Pre-insert an older token into globalDB.
	_, err = globalDB.ExecContext(ctx, `
		INSERT INTO mcp_oauth_tokens
			(server_name, server_url, access_token, token_type, client_id, created_at, updated_at)
		VALUES ('github-mcp', 'https://github.com', 'old-token', 'Bearer', 'client-old', 50, 100)
	`)
	require.NoError(t, err)

	// Create source with a newer token.
	workingDir := t.TempDir()
	sourceDir := filepath.Join(workingDir, ".anvil")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	sourceDB, err := db.Connect(ctx, sourceDir)
	require.NoError(t, err)

	_, err = sourceDB.ExecContext(ctx, `
		INSERT INTO mcp_oauth_tokens
			(server_name, server_url, access_token, token_type, client_id, created_at, updated_at)
		VALUES ('github-mcp', 'https://github.com', 'new-token', 'Bearer', 'client-new', 100, 300)
	`)
	require.NoError(t, err)

	sourcePath := filepath.Join(sourceDir, "anvil.db")
	require.NoError(t, db.Release(sourceDir))

	err = migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	// The newer token should win.
	var accessToken string
	err = globalDB.QueryRowContext(ctx,
		`SELECT access_token FROM mcp_oauth_tokens WHERE server_name = 'github-mcp'`,
	).Scan(&accessToken)
	require.NoError(t, err)
	require.Equal(t, "new-token", accessToken)

	// Now create a second source with an even older token.
	workingDir2 := t.TempDir()
	sourceDir2 := filepath.Join(workingDir2, ".anvil")
	require.NoError(t, os.MkdirAll(sourceDir2, 0o755))
	sourceDB2, err := db.Connect(ctx, sourceDir2)
	require.NoError(t, err)

	_, err = sourceDB2.ExecContext(ctx, `
		INSERT INTO mcp_oauth_tokens
			(server_name, server_url, access_token, token_type, client_id, created_at, updated_at)
		VALUES ('github-mcp', 'https://github.com', 'oldest-token', 'Bearer', 'client-oldest', 10, 50)
	`)
	require.NoError(t, err)

	sourcePath2 := filepath.Join(sourceDir2, "anvil.db")
	require.NoError(t, db.Release(sourceDir2))

	err = migrate.ProjectDB(ctx, globalDB, sourcePath2, workingDir2, 0)
	require.NoError(t, err)

	// The newer token should still be there.
	err = globalDB.QueryRowContext(ctx,
		`SELECT access_token FROM mcp_oauth_tokens WHERE server_name = 'github-mcp'`,
	).Scan(&accessToken)
	require.NoError(t, err)
	require.Equal(t, "new-token", accessToken)
}

func TestMigrateReadFilesPathConversion(t *testing.T) {
	t.Parallel()

	globalDB, sourcePath, workingDir := setupTestDBs(t)
	ctx := context.Background()

	err := migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	// Check relative path was converted to absolute.
	var path string
	err = globalDB.QueryRowContext(ctx,
		`SELECT path FROM read_files WHERE session_id = 'sess-1' AND path LIKE '%main.go'`,
	).Scan(&path)
	require.NoError(t, err)
	require.Equal(t, workingDir+"/src/main.go", path)

	// Check absolute path was left unchanged.
	err = globalDB.QueryRowContext(ctx,
		`SELECT path FROM read_files WHERE session_id = 'sess-1' AND path LIKE '/absolute%'`,
	).Scan(&path)
	require.NoError(t, err)
	require.Equal(t, "/absolute/path.go", path)
}

func TestMigrateIsMigrated(t *testing.T) {
	t.Parallel()

	globalDB, sourcePath, workingDir := setupTestDBs(t)
	ctx := context.Background()

	// Before migration.
	migrated, err := migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.False(t, migrated)

	// After migration.
	err = migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	migrated, err = migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.True(t, migrated)
}

func TestMigrateMissingSourceSkipped(t *testing.T) {
	t.Parallel()

	globalDir := t.TempDir()
	ctx := context.Background()
	globalDB, err := db.Connect(ctx, globalDir)
	require.NoError(t, err)

	// Source path does not exist.
	err = migrate.ProjectDB(ctx, globalDB, "/nonexistent/anvil.db", "/nonexistent", 0)
	require.NoError(t, err, "missing source DB should be skipped gracefully")

	// Verify a migration marker was inserted so subsequent startups
	// skip this project without re-stat'ing it.
	migrated, err := migrate.IsMigrated(ctx, globalDB, "/nonexistent/anvil.db")
	require.NoError(t, err)
	require.True(t, migrated)
}

func TestMigrateMissingSourceResetAndRemark(t *testing.T) {
	t.Parallel()

	globalDir := t.TempDir()
	ctx := context.Background()
	globalDB, err := db.Connect(ctx, globalDir)
	require.NoError(t, err)
	t.Cleanup(func() { db.Release(globalDir) })

	const sourcePath = "/nonexistent/anvil.db"

	// First call: marks the missing source.
	err = migrate.ProjectDB(ctx, globalDB, sourcePath, "/nonexistent", 0)
	require.NoError(t, err)

	migrated, err := migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.True(t, migrated)

	// Simulate --force-migration clearing markers.
	_, err = migrate.ResetAllMigrations(ctx, globalDB)
	require.NoError(t, err)

	migrated, err = migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.False(t, migrated)

	// Second call after reset: marker must be re-created.
	err = migrate.ProjectDB(ctx, globalDB, sourcePath, "/nonexistent", 0)
	require.NoError(t, err)

	migrated, err = migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.True(t, migrated)
}

// setupProjectsEnv redirects projects.List() to a temp directory by
// setting ANVIL_GLOBAL_DATA, and returns a helper that registers a
// project entry in the resulting projects.json. Must not be used with
// t.Parallel() since it modifies a process-wide env var.
func setupProjectsEnv(t *testing.T) (registerProject func(workingDir, dataDir string)) {
	t.Helper()

	anvilDataDir := filepath.Join(t.TempDir(), "anvil")
	require.NoError(t, os.MkdirAll(anvilDataDir, 0o755))
	// ANVIL_GLOBAL_DATA is the parent dir; GlobalConfigData() appends
	// "anvil.json" to produce the full path.
	t.Setenv("ANVIL_GLOBAL_DATA", anvilDataDir)

	return func(workingDir, dataDir string) {
		t.Helper()
		require.NoError(t, projects.Register(workingDir, dataDir))
	}
}

func TestAllProjectsMissingSourceSecondPassSkips(t *testing.T) {
	// Not parallel: uses t.Setenv for ANVIL_GLOBAL_DATA.
	registerProject := setupProjectsEnv(t)

	ctx := context.Background()

	globalDir := t.TempDir()
	globalDB, err := db.Connect(ctx, globalDir)
	require.NoError(t, err)
	t.Cleanup(func() { db.Release(globalDir) })

	// Create a project directory with no source DB.
	workingDir := t.TempDir()
	projectDataDir := filepath.Join(workingDir, ".anvil")
	require.NoError(t, os.MkdirAll(projectDataDir, 0o755))
	registerProject(workingDir, projectDataDir)

	// First AllProjects pass: processes the project and marks it.
	err = migrate.AllProjects(ctx, globalDB, "")
	require.NoError(t, err)

	var rowCount1 int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM migrations_completed`).Scan(&rowCount1)
	require.NoError(t, err)
	require.Equal(t, 1, rowCount1, "first pass must insert exactly one marker")

	// Second AllProjects pass: IsMigrated is true, so no inserts.
	err = migrate.AllProjects(ctx, globalDB, "")
	require.NoError(t, err)

	var rowCount2 int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM migrations_completed`).Scan(&rowCount2)
	require.NoError(t, err)
	require.Equal(t, rowCount1, rowCount2, "second pass must not insert additional markers")
}

func TestCurrentProjectThenAllProjectsMarkerConsistency(t *testing.T) {
	// Not parallel: uses t.Setenv for ANVIL_GLOBAL_DATA.
	registerProject := setupProjectsEnv(t)

	ctx := context.Background()

	globalDir := t.TempDir()
	globalDB, err := db.Connect(ctx, globalDir)
	require.NoError(t, err)
	t.Cleanup(func() { db.Release(globalDir) })

	// Create a project directory with no source DB.
	workingDir := t.TempDir()
	projectDataDir := filepath.Join(workingDir, ".anvil")
	require.NoError(t, os.MkdirAll(projectDataDir, 0o755))
	registerProject(workingDir, projectDataDir)

	// CurrentProject processes the project and marks it via ProjectDB.
	err = migrate.CurrentProject(ctx, globalDB, projectDataDir)
	require.NoError(t, err)

	// Verify marker is present before AllProjects runs.
	sourcePath := filepath.Join(projectDataDir, "anvil.db")
	migrated, err := migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.True(t, migrated, "CurrentProject must mark missing-source project")

	var rowBefore int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM migrations_completed`).Scan(&rowBefore)
	require.NoError(t, err)

	// AllProjects should skip this project (marker path consistent
	// with CurrentProject's derived sourcePath).
	err = migrate.AllProjects(ctx, globalDB, "")
	require.NoError(t, err)

	var rowAfter int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM migrations_completed`).Scan(&rowAfter)
	require.NoError(t, err)
	require.Equal(t, rowBefore, rowAfter,
		"AllProjects must not re-insert a marker already written by CurrentProject")
}

func TestMigrateEvalSymlinksNormalization(t *testing.T) {
	t.Parallel()

	globalDB, sourcePath, workingDir := setupTestDBs(t)
	ctx := context.Background()

	// Use workingDir as-is (EvalSymlinks resolves it). Verify the
	// stored working_dir matches the resolved path.
	err := migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	resolved, err := filepath.EvalSymlinks(workingDir)
	require.NoError(t, err)

	var storedDir string
	err = globalDB.QueryRowContext(ctx,
		`SELECT working_dir FROM sessions WHERE id = 'sess-1'`).Scan(&storedDir)
	require.NoError(t, err)
	require.Equal(t, resolved, storedDir)
}

func TestMigrateCurrentProject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	globalDir := t.TempDir()
	globalDB, err := db.Connect(ctx, globalDir)
	require.NoError(t, err)
	t.Cleanup(func() { db.Release(globalDir) })

	// Create a project directory with a source DB.
	workingDir := t.TempDir()
	projectDir := filepath.Join(workingDir, ".anvil")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	sourceDB, err := db.Connect(ctx, projectDir)
	require.NoError(t, err)

	_, err = sourceDB.ExecContext(ctx, `
		INSERT INTO sessions (id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at, working_dir)
		VALUES ('sess-startup', 'Startup Test', 0, 0, 0, 0.0, 100, 100, ?)
	`, workingDir)
	require.NoError(t, err)
	require.NoError(t, db.Release(projectDir))

	err = migrate.CurrentProject(ctx, globalDB, projectDir)
	require.NoError(t, err)

	// Verify the session is in the global DB.
	var title string
	err = globalDB.QueryRowContext(ctx,
		`SELECT title FROM sessions WHERE id = 'sess-startup'`).Scan(&title)
	require.NoError(t, err)
	require.Equal(t, "Startup Test", title)

	// Second call should be a no-op.
	err = migrate.CurrentProject(ctx, globalDB, projectDir)
	require.NoError(t, err)
}

func TestMigrateCurrentProjectSkipsMissing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	globalDir := t.TempDir()
	globalDB, err := db.Connect(ctx, globalDir)
	require.NoError(t, err)
	t.Cleanup(func() { db.Release(globalDir) })

	// Non-existent project directory.
	err = migrate.CurrentProject(ctx, globalDB, "/nonexistent/.anvil")
	require.NoError(t, err, "should not error for missing source DB")
}

// TestMigrateBatchedPartialFailureRerun verifies that re-running a
// batched migration after a simulated partial failure produces
// correct results. This exercises the INSERT OR IGNORE safety net:
// already-copied rows are skipped, remaining rows are picked up,
// and session counts are corrected by the final overwrite step.
func TestMigrateBatchedPartialFailureRerun(t *testing.T) {
	t.Parallel()

	globalDB, sourcePath, workingDir := setupTestDBs(t)
	ctx := context.Background()

	// Simulate a "partial migration" by manually inserting only
	// sess-1 (with wrong message_count) and its first message into
	// the global DB, as if batched mode crashed after copying some
	// sessions and messages but before completing.
	_, err := globalDB.ExecContext(ctx, `
		INSERT INTO sessions
			(id, title, message_count, prompt_tokens, completion_tokens,
			 cost, updated_at, created_at, working_dir)
		VALUES ('sess-1', 'Session One', 999, 100, 50, 0.01, 1000000, 900000, ?)
	`, workingDir)
	require.NoError(t, err)

	_, err = globalDB.ExecContext(ctx, `
		INSERT INTO messages (id, session_id, role, parts, created_at, updated_at, message_type)
		VALUES ('msg-1', 'sess-1', 'user', '["hello"]', 900001, 900001, 'message')
	`)
	require.NoError(t, err)

	// Now run the full batched migration. It should:
	// - Skip sess-1 (INSERT OR IGNORE), insert sess-2
	// - Skip msg-1 (INSERT OR IGNORE), insert msg-2..msg-4
	// - Overwrite message_count from source (fixing the 999 → 3)
	// - Copy files, read_files, OAuth
	err = migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 100)
	require.NoError(t, err)

	// Verify session count — both sessions should exist.
	var sessionCount int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount)
	require.NoError(t, err)
	require.Equal(t, 2, sessionCount)

	// Verify message_count was corrected from 999 → 3.
	var msgCount int64
	err = globalDB.QueryRowContext(ctx,
		`SELECT message_count FROM sessions WHERE id = 'sess-1'`).Scan(&msgCount)
	require.NoError(t, err)
	require.Equal(t, int64(3), msgCount)

	// Verify all 4 messages are present.
	var totalMsgs int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages`).Scan(&totalMsgs)
	require.NoError(t, err)
	require.Equal(t, 4, totalMsgs)

	// Verify files were copied.
	var fileCount int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM files`).Scan(&fileCount)
	require.NoError(t, err)
	require.Equal(t, 1, fileCount)

	// Verify migration is now marked complete.
	migrated, err := migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.True(t, migrated)
}

func TestResetMigration(t *testing.T) {
	t.Parallel()

	globalDB, sourcePath, workingDir := setupTestDBs(t)
	ctx := context.Background()

	// Migrate once.
	err := migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	migrated, err := migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.True(t, migrated)

	// Reset the migration marker.
	err = migrate.ResetMigration(ctx, globalDB, sourcePath)
	require.NoError(t, err)

	// Should no longer be marked as migrated.
	migrated, err = migrate.IsMigrated(ctx, globalDB, sourcePath)
	require.NoError(t, err)
	require.False(t, migrated)

	// Re-run should succeed (INSERT OR IGNORE skips existing data).
	err = migrate.ProjectDB(ctx, globalDB, sourcePath, workingDir, 0)
	require.NoError(t, err)

	// Verify data integrity after re-run — no duplicates.
	var count int
	err = globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	var msgCount int64
	err = globalDB.QueryRowContext(ctx,
		`SELECT message_count FROM sessions WHERE id = 'sess-1'`).Scan(&msgCount)
	require.NoError(t, err)
	require.Equal(t, int64(3), msgCount)
}

func TestResetAllMigrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	globalDir := t.TempDir()
	globalDB, err := db.Connect(ctx, globalDir)
	require.NoError(t, err)
	t.Cleanup(func() { db.Release(globalDir) })

	// Insert two fake migration markers.
	_, err = globalDB.ExecContext(ctx,
		`INSERT INTO migrations_completed (source_path) VALUES ('/a/anvil.db'), ('/b/anvil.db')`)
	require.NoError(t, err)

	count, err := migrate.ResetAllMigrations(ctx, globalDB)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	// Both should be cleared.
	migratedA, _ := migrate.IsMigrated(ctx, globalDB, "/a/anvil.db")
	migratedB, _ := migrate.IsMigrated(ctx, globalDB, "/b/anvil.db")
	require.False(t, migratedA)
	require.False(t, migratedB)
}

// Package migrate handles one-time data migration from per-project
// SQLite databases into the global database. It is separate from
// internal/db (which owns connections, sqlc queries, and schema
// migrations) because data migration is application-level
// orchestration with business logic (OAuth conflict resolution,
// path normalization) and external dependencies (project discovery).
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Broderick-Westrope/anvil/internal/db"
)

// ProjectDB migrates a single project's SQLite database into the
// global database. sourcePath is the absolute path to the source
// anvil.db file. workingDir is the project's working directory (used
// to populate sessions.working_dir and to convert read_files relative
// paths to absolute).
//
// batchSize controls the migration strategy:
//   - 0: synchronous single-transaction mode. Triggers are dropped
//     before copying to avoid message_count/updated_at corruption,
//     then recreated after.
//   - >0: batched mode. Sessions are inserted with message_count=0,
//     messages are copied in batches (triggers fire correctly from 0),
//     then sessions are updated with the original message_count and
//     updated_at.
//
// The function uses SQLite ATTACH DATABASE to read from the source DB
// within the global DB's connection, avoiding shuttling data through
// Go. It uses (*sql.DB).Conn(ctx) to pin a single connection for the
// ATTACH/query/DETACH sequence — this is required because
// MaxOpenConns(1) does not guarantee the same underlying connection
// across separate Exec calls on *sql.DB.
func ProjectDB(ctx context.Context, globalDB *sql.DB, sourcePath, workingDir string, batchSize int) error {
	if globalDB == nil {
		return fmt.Errorf("globalDB is nil")
	}

	// Normalize workingDir with EvalSymlinks (e.g. macOS /var vs
	// /private/var).
	resolved, err := filepath.EvalSymlinks(workingDir)
	if err == nil {
		workingDir = resolved
	}

	// Check if source DB exists; skip gracefully if missing.
	if _, err := os.Stat(sourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			slog.Debug("Source DB does not exist, marking as migrated", "path", sourcePath)
			// Record the marker so subsequent startups skip this
			// project entirely instead of re-stat'ing it on every
			// launch. If an older Anvil later creates this DB it will
			// be skipped until --force-migration clears the markers;
			// per-project DBs are legacy so this is accepted.
			if _, err := globalDB.ExecContext(ctx,
				`INSERT OR IGNORE INTO migrations_completed (source_path) VALUES (?)`,
				sourcePath,
			); err != nil {
				return fmt.Errorf("failed to record missing-source migration: %w", err)
			}
			return nil
		}
		return fmt.Errorf("failed to stat source DB %q: %w", sourcePath, err)
	}

	// Run goose on source DB to bring it to current schema.
	sourceDB, err := db.OpenAndMigrateSource(ctx, sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open source DB: %w", err)
	}
	defer sourceDB.Close()

	// Pin a single connection for the ATTACH/query/DETACH sequence.
	// SQLite ATTACH operates on the connection level, and although
	// MaxOpenConns(1) means only one connection exists in the pool,
	// (*sql.DB).Exec may release and re-acquire it between calls.
	// Pinning via Conn(ctx) guarantees all operations use the same
	// underlying connection.
	conn, err := globalDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to pin connection: %w", err)
	}
	defer conn.Close()

	// Attach source database.
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS source`, sourcePath); err != nil {
		return fmt.Errorf("failed to attach source DB: %w", err)
	}
	defer func() {
		// Use context.Background() so DETACH runs even if the parent
		// context is canceled. Otherwise the connection returns to the
		// pool with the source DB still attached.
		if _, detachErr := conn.ExecContext(context.Background(), `DETACH DATABASE source`); detachErr != nil {
			slog.Error("Failed to detach source DB", "error", detachErr)
		}
	}()

	if batchSize <= 0 {
		return migrateSynchronous(ctx, conn, workingDir, sourcePath)
	}
	return migrateBatched(ctx, conn, workingDir, sourcePath, batchSize)
}

// IsMigrated checks whether the source DB at sourcePath has already
// been migrated into the global database.
func IsMigrated(ctx context.Context, globalDB *sql.DB, sourcePath string) (bool, error) {
	var count int
	err := globalDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM migrations_completed WHERE source_path = ?`,
		sourcePath,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check migration status: %w", err)
	}
	return count > 0, nil
}

// ResetMigration removes the completion marker for the given source
// path, causing the next startup to re-run the migration. This is safe
// because all data copy operations use INSERT OR IGNORE, so re-runs
// skip existing rows without creating duplicates.
func ResetMigration(ctx context.Context, globalDB *sql.DB, sourcePath string) error {
	_, err := globalDB.ExecContext(ctx,
		`DELETE FROM migrations_completed WHERE source_path = ?`,
		sourcePath,
	)
	if err != nil {
		return fmt.Errorf("failed to reset migration for %q: %w", sourcePath, err)
	}
	return nil
}

// ResetAllMigrations removes all completion markers, causing the next
// startup to re-run every project migration.
func ResetAllMigrations(ctx context.Context, globalDB *sql.DB) (int64, error) {
	result, err := globalDB.ExecContext(ctx,
		`DELETE FROM migrations_completed`,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to reset all migrations: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get affected rows: %w", err)
	}
	return count, nil
}

// sessionTriggerNames lists the triggers that interfere with bulk
// migration of sessions and messages. These are saved from
// sqlite_master before dropping and restored after copying.
var sessionTriggerNames = []string{
	"update_session_message_count_on_insert",
	"update_session_message_count_on_delete",
	"update_sessions_updated_at",
}

// migrateSynchronous performs the migration in a single transaction
// with triggers dropped to avoid message_count/updated_at corruption.
func migrateSynchronous(ctx context.Context, conn *sql.Conn, workingDir, sourcePath string) error {
	// Capture existing trigger DDL from sqlite_master before the
	// transaction so we can restore them exactly after copying.
	saved, err := saveTriggers(ctx, conn, sessionTriggerNames)
	if err != nil {
		return fmt.Errorf("failed to save triggers: %w", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Drop triggers to prevent message_count inflation and
	// updated_at clobbering during bulk insert.
	if err := dropTriggers(ctx, tx, saved); err != nil {
		return err
	}

	// Copy sessions with working_dir.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO main.sessions
			(id, parent_session_id, title, message_count, prompt_tokens,
			 completion_tokens, cost, updated_at, created_at, todos,
			 leaf_message_id, working_dir)
		SELECT
			id, parent_session_id, title, message_count, prompt_tokens,
			completion_tokens, cost, updated_at, created_at, todos,
			leaf_message_id, ?
		FROM source.sessions
	`, workingDir); err != nil {
		return fmt.Errorf("failed to copy sessions: %w", err)
	}

	// Copy messages.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO main.messages
			(id, session_id, role, parts, model, created_at, updated_at,
			 finished_at, provider, parent_message_id, message_type)
		SELECT
			id, session_id, role, parts, model, created_at, updated_at,
			finished_at, provider, parent_message_id, message_type
		FROM source.messages
	`); err != nil {
		return fmt.Errorf("failed to copy messages: %w", err)
	}

	// Copy files.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO main.files
			(id, session_id, path, content, version, created_at, updated_at)
		SELECT
			id, session_id, path, content, version, created_at, updated_at
		FROM source.files
	`); err != nil {
		return fmt.Errorf("failed to copy files: %w", err)
	}

	// Copy read_files, converting relative paths to absolute by
	// prepending workingDir. Paths already absolute (Unix '/',
	// Windows drive letter 'C:\', or UNC '\\') are copied as-is.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO main.read_files (session_id, path, read_at)
		SELECT
			session_id,
			CASE
				WHEN path LIKE '/%' THEN path
				WHEN path LIKE '_:\%' THEN path
				WHEN path LIKE '_:/%' THEN path
				WHEN path LIKE '\\%' THEN path
				ELSE ? || '/' || path
			END,
			read_at
		FROM source.read_files
	`, workingDir); err != nil {
		return fmt.Errorf("failed to copy read_files: %w", err)
	}

	// Copy mcp_oauth_tokens with newest-wins conflict resolution.
	// INSERT...SELECT...ON CONFLICT is not supported across attached
	// databases in all SQLite drivers, so we use INSERT OR IGNORE
	// followed by a conditional UPDATE.
	if err := copyOAuthTokens(ctx, tx); err != nil {
		return err
	}

	// Copy mcp_oauth_clients with newest-wins conflict resolution.
	if err := copyOAuthClients(ctx, tx); err != nil {
		return err
	}

	// Restore triggers from their saved DDL.
	if err := restoreTriggers(ctx, tx, saved); err != nil {
		return err
	}

	// Mark migration as completed.
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO migrations_completed (source_path) VALUES (?)`,
		sourcePath,
	); err != nil {
		return fmt.Errorf("failed to record migration: %w", err)
	}

	return tx.Commit()
}

// migrateBatched performs the migration in batches, yielding the write
// lock between transactions to avoid starving active sessions.
//
// Partial failure safety: if this function fails midway, the
// migrations_completed marker is NOT written (it is the last step).
// On the next startup IsMigrated returns false and the migration
// re-runs from the beginning. All INSERT operations use INSERT OR
// IGNORE, so rows already present are skipped without error or
// duplication. The session count overwrite (step 3) is idempotent.
// This means re-runs are always safe, just slower than a fresh run
// because already-migrated rows are re-scanned before being skipped.
func migrateBatched(ctx context.Context, conn *sql.Conn, workingDir, sourcePath string, batchSize int) error {
	slog.Info("Starting batched migration", "source", sourcePath, "working_dir", workingDir)

	// Copy sessions with message_count=0 so trigger-based increments
	// during message insertion start from zero.
	if _, err := conn.ExecContext(ctx, `
		INSERT OR IGNORE INTO main.sessions
			(id, parent_session_id, title, message_count, prompt_tokens,
			 completion_tokens, cost, updated_at, created_at, todos,
			 leaf_message_id, working_dir)
		SELECT
			id, parent_session_id, title, 0, prompt_tokens,
			completion_tokens, cost, updated_at, created_at, todos,
			leaf_message_id, ?
		FROM source.sessions
	`, workingDir); err != nil {
		return fmt.Errorf("failed to copy sessions: %w", err)
	}
	slog.Info("Batched migration: sessions copied", "source", sourcePath)

	// Copy messages in batches. Triggers fire and increment
	// message_count from 0.
	if err := copyInBatches(ctx, conn, `
		INSERT OR IGNORE INTO main.messages
			(id, session_id, role, parts, model, created_at, updated_at,
			 finished_at, provider, parent_message_id, message_type)
		SELECT
			id, session_id, role, parts, model, created_at, updated_at,
			finished_at, provider, parent_message_id, message_type
		FROM source.messages
		ORDER BY rowid
		LIMIT ? OFFSET ?
	`, batchSize, "messages"); err != nil {
		return err
	}
	slog.Info("Batched migration: messages copied", "source", sourcePath)

	// Overwrite message_count and updated_at with source values to
	// correct trigger artifacts. The update_sessions_updated_at
	// trigger must be temporarily dropped to prevent it from
	// clobbering the source updated_at value.
	updatedAtTrigger, err := saveTriggers(ctx, conn, []string{"update_sessions_updated_at"})
	if err != nil {
		return fmt.Errorf("failed to save sessions trigger: %w", err)
	}
	if err := dropTriggers(ctx, conn, updatedAtTrigger); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, `
		UPDATE main.sessions
		SET
			message_count = (
				SELECT s.message_count FROM source.sessions s
				WHERE s.id = main.sessions.id
			),
			updated_at = (
				SELECT s.updated_at FROM source.sessions s
				WHERE s.id = main.sessions.id
			)
		WHERE id IN (SELECT id FROM source.sessions)
	`); err != nil {
		return fmt.Errorf("failed to overwrite session counts: %w", err)
	}

	if err := restoreTriggers(ctx, conn, updatedAtTrigger); err != nil {
		return err
	}
	slog.Info("Batched migration: session counts corrected", "source", sourcePath)

	// Copy files in batches.
	if err := copyInBatches(ctx, conn, `
		INSERT OR IGNORE INTO main.files
			(id, session_id, path, content, version, created_at, updated_at)
		SELECT
			id, session_id, path, content, version, created_at, updated_at
		FROM source.files
		ORDER BY rowid
		LIMIT ? OFFSET ?
	`, batchSize, "files"); err != nil {
		return err
	}
	slog.Info("Batched migration: files copied", "source", sourcePath)

	// Copy read_files in batches with relative→absolute path
	// conversion.
	if err := copyReadFilesBatched(ctx, conn, workingDir, batchSize); err != nil {
		return err
	}
	slog.Info("Batched migration: read_files copied", "source", sourcePath)

	// Copy OAuth tables (typically small, single transaction is fine).
	if err := copyOAuthTokens(ctx, conn); err != nil {
		return err
	}

	if err := copyOAuthClients(ctx, conn); err != nil {
		return err
	}
	slog.Info("Batched migration: OAuth data copied", "source", sourcePath)

	// Mark migration as completed.
	if _, err := conn.ExecContext(ctx,
		`INSERT OR IGNORE INTO migrations_completed (source_path) VALUES (?)`,
		sourcePath,
	); err != nil {
		return fmt.Errorf("failed to record migration: %w", err)
	}

	return nil
}

// copyInBatches executes a parameterized query with LIMIT/OFFSET in
// batches until no more rows are affected.
func copyInBatches(ctx context.Context, conn *sql.Conn, query string, batchSize int, table string) error {
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		result, err := conn.ExecContext(ctx, query, batchSize, offset)
		if err != nil {
			return fmt.Errorf("failed to copy %s at offset %d: %w", table, offset, err)
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get rows affected for %s: %w", table, err)
		}

		if affected == 0 {
			break
		}

		offset += batchSize
	}
	return nil
}

// execer abstracts over *sql.Tx and *sql.Conn for query execution.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// savedTrigger holds a trigger's name and the DDL needed to recreate
// it, as read from sqlite_master.
type savedTrigger struct {
	Name string
	SQL  string
}

// saveTriggers reads the CREATE TRIGGER DDL for the named triggers
// from sqlite_master. Triggers that don't exist are silently skipped.
func saveTriggers(ctx context.Context, conn *sql.Conn, names []string) ([]savedTrigger, error) {
	var saved []savedTrigger
	for _, name := range names {
		var ddl string
		err := conn.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`,
			name,
		).Scan(&ddl)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read trigger %q from sqlite_master: %w", name, err)
		}
		saved = append(saved, savedTrigger{Name: name, SQL: ddl})
	}
	return saved, nil
}

// dropTriggers drops the given triggers by name.
func dropTriggers(ctx context.Context, e execer, triggers []savedTrigger) error {
	for _, t := range triggers {
		if _, err := e.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS "%s"`, t.Name)); err != nil {
			return fmt.Errorf("failed to drop trigger %s: %w", t.Name, err)
		}
	}
	return nil
}

// restoreTriggers recreates triggers from their saved DDL.
func restoreTriggers(ctx context.Context, e execer, triggers []savedTrigger) error {
	for _, t := range triggers {
		// sqlite_master strips IF NOT EXISTS from stored DDL, so
		// drop before recreating as a defensive guard.
		if _, err := e.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS "%s"`, t.Name)); err != nil {
			return fmt.Errorf("failed to drop trigger %s before restore: %w", t.Name, err)
		}
		if _, err := e.ExecContext(ctx, t.SQL); err != nil {
			return fmt.Errorf("failed to recreate trigger %s: %w", t.Name, err)
		}
	}
	return nil
}

// copyOAuthTokens copies mcp_oauth_tokens from source to main using
// INSERT OR IGNORE followed by a conditional UPDATE to implement
// newest-wins conflict resolution. This two-step approach is needed
// because INSERT...SELECT...ON CONFLICT is not reliably supported
// across attached databases in all SQLite drivers.
//
// NOTE: If two Anvil processes migrate the same project concurrently,
// the INSERT OR IGNORE + UPDATE sequence is not atomic across processes.
// This is accepted behavior — migrations_completed prevents full
// re-migration, and the worst case is a slightly stale token that will
// be refreshed on next use.
func copyOAuthTokens(ctx context.Context, e execer) error {
	// Insert any tokens that don't exist yet.
	if _, err := e.ExecContext(ctx, `
		INSERT OR IGNORE INTO main.mcp_oauth_tokens
			(server_name, server_url, access_token, refresh_token, token_type,
			 expiry, scopes, token_endpoint, client_id, client_secret,
			 created_at, updated_at)
		SELECT
			server_name, server_url, access_token, refresh_token, token_type,
			expiry, scopes, token_endpoint, client_id, client_secret,
			created_at, updated_at
		FROM source.mcp_oauth_tokens
	`); err != nil {
		return fmt.Errorf("failed to insert mcp_oauth_tokens: %w", err)
	}

	// Update existing tokens where source is newer.
	if _, err := e.ExecContext(ctx, `
		UPDATE main.mcp_oauth_tokens
		SET
			server_url     = s.server_url,
			access_token   = s.access_token,
			refresh_token  = s.refresh_token,
			token_type     = s.token_type,
			expiry         = s.expiry,
			scopes         = s.scopes,
			token_endpoint = s.token_endpoint,
			client_id      = s.client_id,
			client_secret  = s.client_secret,
			updated_at     = s.updated_at
		FROM source.mcp_oauth_tokens s
		WHERE main.mcp_oauth_tokens.server_name = s.server_name
			AND s.updated_at > main.mcp_oauth_tokens.updated_at
	`); err != nil {
		return fmt.Errorf("failed to update mcp_oauth_tokens: %w", err)
	}

	return nil
}

// copyOAuthClients copies mcp_oauth_clients from source to main using
// INSERT OR IGNORE followed by a conditional UPDATE for newest-wins.
func copyOAuthClients(ctx context.Context, e execer) error {
	if _, err := e.ExecContext(ctx, `
		INSERT OR IGNORE INTO main.mcp_oauth_clients
			(server_name, server_url, client_id, client_secret, created_at)
		SELECT
			server_name, server_url, client_id, client_secret, created_at
		FROM source.mcp_oauth_clients
	`); err != nil {
		return fmt.Errorf("failed to insert mcp_oauth_clients: %w", err)
	}

	if _, err := e.ExecContext(ctx, `
		UPDATE main.mcp_oauth_clients
		SET
			server_url    = s.server_url,
			client_id     = s.client_id,
			client_secret = s.client_secret,
			created_at    = s.created_at
		FROM source.mcp_oauth_clients s
		WHERE main.mcp_oauth_clients.server_name = s.server_name
			AND s.created_at > main.mcp_oauth_clients.created_at
	`); err != nil {
		return fmt.Errorf("failed to update mcp_oauth_clients: %w", err)
	}

	return nil
}

// copyReadFilesBatched copies read_files in batches with
// relative-to-absolute path conversion.
func copyReadFilesBatched(ctx context.Context, conn *sql.Conn, workingDir string, batchSize int) error {
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		result, err := conn.ExecContext(ctx, `
			INSERT OR IGNORE INTO main.read_files (session_id, path, read_at)
			SELECT
				session_id,
				CASE
					WHEN path LIKE '/%' THEN path
					WHEN path LIKE '_:\%' THEN path
					WHEN path LIKE '_:/%' THEN path
					WHEN path LIKE '\\%' THEN path
					ELSE ? || '/' || path
				END,
				read_at
			FROM source.read_files
			ORDER BY rowid
			LIMIT ? OFFSET ?
		`, workingDir, batchSize, offset)
		if err != nil {
			return fmt.Errorf("failed to copy read_files at offset %d: %w", offset, err)
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get rows affected for read_files: %w", err)
		}

		if affected == 0 {
			break
		}

		offset += batchSize
	}
	return nil
}

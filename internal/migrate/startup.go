package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/projects"
)

// CurrentProject performs a synchronous (single-transaction) migration
// of the current project's .anvil/anvil.db into the global database.
// It is intended to run at startup before the session begins so the
// current project's data is immediately available.
func CurrentProject(ctx context.Context, globalDB *sql.DB, projectDir string) error {
	sourcePath := filepath.Join(projectDir, "anvil.db")

	migrated, err := IsMigrated(ctx, globalDB, sourcePath)
	if err != nil {
		return fmt.Errorf("failed to check migration status: %w", err)
	}
	if migrated {
		return nil
	}

	// workingDir is the parent of the .anvil directory.
	workingDir := filepath.Dir(projectDir)

	if err := ProjectDB(ctx, globalDB, sourcePath, workingDir, 0); err != nil {
		return fmt.Errorf("failed to migrate current project: %w", err)
	}

	return nil
}

// AllProjects iterates all projects registered in projects.json and
// migrates each one into the global database using batched mode
// (batchSize=500). skipCurrent is the project directory that was
// already migrated synchronously and should be skipped. Projects are
// migrated sequentially with a 50ms sleep between them to avoid
// starving active session writes. The function respects context
// cancellation.
func AllProjects(ctx context.Context, globalDB *sql.DB, skipCurrent string) error {
	projectList, err := projects.List()
	if err != nil {
		return fmt.Errorf("failed to load projects list: %w", err)
	}

	for _, p := range projectList {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Skip the project that was already migrated synchronously.
		if p.DataDir == skipCurrent {
			continue
		}

		sourcePath := filepath.Join(p.DataDir, "anvil.db")

		migrated, err := IsMigrated(ctx, globalDB, sourcePath)
		if err != nil {
			slog.Error("Failed to check migration status",
				"project", p.Path, "error", err)
			continue
		}
		if migrated {
			continue
		}

		if err := ProjectDB(ctx, globalDB, sourcePath, p.Path, 500); err != nil {
			slog.Error("Failed to migrate project",
				"project", p.Path, "error", err)
			continue
		}

		slog.Info("Migrated project to global DB", "project", p.Path)

		// Yield between projects to reduce write lock contention.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}

	return nil
}

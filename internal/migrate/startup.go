package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
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

// AllProjects is a no-op; the projects package has been removed.
// This function will be deleted together with the migrate package in
// the next cleanup phase.
func AllProjects(_ context.Context, _ *sql.DB, _ string) error {
	return nil
}

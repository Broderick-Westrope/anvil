// Package filetracker provides functionality to track file reads in sessions.
package filetracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
)

// Service defines the interface for tracking file reads in sessions.
type Service interface {
	// RecordRead records when a file was read, computing the content hash
	// from the file's current raw bytes on disk. If the file cannot be
	// read, the read is recorded with an empty hash.
	RecordRead(ctx context.Context, sessionID, path string)

	// RecordReadWithHash records when a file was read using a precomputed
	// content hash, avoiding a redundant disk read.
	RecordReadWithHash(ctx context.Context, sessionID, path, hash string)

	// LastReadTime returns when a file was last read.
	// Returns zero time if never read.
	LastReadTime(ctx context.Context, sessionID, path string) time.Time

	// LastContentHash returns the content hash recorded for the last read
	// of a file. Returns "" when the file has never been seen.
	LastContentHash(ctx context.Context, sessionID, path string) string

	// ListReadFiles returns the absolute paths of all files read in a session.
	ListReadFiles(ctx context.Context, sessionID string) ([]string, error)
}

// HashContent returns the hex-encoded SHA-256 hash of raw content bytes.
func HashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

type service struct {
	q *db.Queries
}

// NewService creates a new file tracker service.
func NewService(q *db.Queries) Service {
	return &service{q: q}
}

// RecordRead records when a file was read, computing the content hash from
// the file's current raw bytes on disk. If the file cannot be read, the read
// is recorded with an empty hash, which downstream consumers treat the same
// as never-seen; the next write-gate check on an existing file will block
// once and re-record the real hash via its error-as-read path.
func (s *service) RecordRead(ctx context.Context, sessionID, path string) {
	hash := ""
	if content, err := os.ReadFile(path); err == nil {
		hash = HashContent(content)
	}
	s.RecordReadWithHash(ctx, sessionID, path, hash)
}

// RecordReadWithHash records when a file was read using a precomputed
// content hash.
func (s *service) RecordReadWithHash(ctx context.Context, sessionID, path, hash string) {
	if err := s.q.RecordFileRead(ctx, db.RecordFileReadParams{
		SessionID:   sessionID,
		Path:        abspath(path),
		ContentHash: hash,
	}); err != nil {
		slog.Error("Error recording file read", "error", err, "file", path)
	}
}

// LastReadTime returns when a file was last read.
// Returns zero time if never read.
func (s *service) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	readFile, err := s.q.GetFileRead(ctx, db.GetFileReadParams{
		SessionID: sessionID,
		Path:      abspath(path),
	})
	if err != nil {
		return time.Time{}
	}

	return time.Unix(readFile.ReadAt, 0)
}

// LastContentHash returns the content hash recorded for the last read of a
// file. Returns "" when the file has never been seen.
func (s *service) LastContentHash(ctx context.Context, sessionID, path string) string {
	readFile, err := s.q.GetFileRead(ctx, db.GetFileReadParams{
		SessionID: sessionID,
		Path:      abspath(path),
	})
	if err != nil {
		return ""
	}

	return readFile.ContentHash
}

// abspath returns the cleaned absolute form of path.
func abspath(path string) string {
	path = filepath.Clean(path)
	if filepath.IsAbs(path) {
		return path
	}
	wd, err := os.Getwd()
	if err != nil {
		slog.Warn("Error getting working directory", "error", err)
		return path
	}
	return filepath.Join(wd, path)
}

// ListReadFiles returns the absolute paths of all files read in a session.
func (s *service) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	readFiles, err := s.q.ListSessionReadFiles(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing read files: %w", err)
	}

	paths := make([]string, 0, len(readFiles))
	for _, rf := range readFiles {
		paths = append(paths, rf.Path)
	}
	return paths, nil
}

package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/lock"
	"github.com/google/uuid"
)

type Entry struct {
	SessionID  string    `json:"session_id"`
	WorkingDir string    `json:"working_dir"`
	Title      string    `json:"title"`
	PID        int       `json:"pid"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Tracker struct {
	mu      sync.Mutex
	updates chan Entry
	done    chan struct{}
	closed  bool
	path    string
	release func()
	err     error
}

func NewTracker(root string) (*Tracker, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("creating recovery directory: %w", err)
	}
	unlockRegistry, err := lockRegistry(root)
	if err != nil {
		return nil, err
	}
	defer unlockRegistry()
	path := filepath.Join(root, uuid.NewString())
	release, err := lock.TryFile(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("locking recovery record: %w", err)
	}
	tracker := &Tracker{
		updates: make(chan Entry, 1), done: make(chan struct{}),
		path: path + ".json", release: release,
	}
	go func() {
		defer close(tracker.done)
		for entry := range tracker.updates {
			if err := tracker.write(entry); err != nil {
				tracker.err = err
				slog.Error("Failed to save session recovery record", "error", err)
			} else {
				tracker.err = nil
			}
		}
	}()
	return tracker, nil
}

func (tracker *Tracker) Track(entry Entry) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.closed {
		return
	}
	entry.PID = os.Getpid()
	entry.UpdatedAt = time.Now().UTC()
	select {
	case <-tracker.updates:
	default:
	}
	tracker.updates <- entry
}

func (tracker *Tracker) Close(clean bool) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.closed {
		return tracker.err
	}
	tracker.closed = true
	close(tracker.updates)
	<-tracker.done
	if clean {
		if err := os.Remove(tracker.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			tracker.err = errors.Join(tracker.err, err)
		}
	}
	unlockRegistry, err := lockRegistry(filepath.Dir(tracker.path))
	tracker.release()
	if err != nil {
		return errors.Join(tracker.err, err)
	}
	defer unlockRegistry()
	tracker.err = errors.Join(tracker.err, sweepArtifacts(filepath.Dir(tracker.path)))
	return tracker.err
}

func (tracker *Tracker) write(entry Entry) (result error) {
	if entry.SessionID == "" {
		if err := os.Remove(tracker.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	file, err := os.CreateTemp(filepath.Dir(tracker.path), strings.TrimSuffix(filepath.Base(tracker.path), ".json")+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	err = json.NewEncoder(file).Encode(entry)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return retryRename(func() error { return os.Rename(file.Name(), tracker.path) })
}

func List(root string) ([]Entry, error) {
	entries := make(map[string]Entry)
	live := make(map[string]bool)
	err := scan(root, true, func(_ string, entry Entry, active bool) error {
		key := entry.WorkingDir + "\x00" + entry.SessionID
		if active {
			live[key] = true
		} else if previous, ok := entries[key]; !ok || entry.UpdatedAt.After(previous.UpdatedAt) {
			entries[key] = entry
		}
		return nil
	})
	result := make([]Entry, 0, len(entries))
	for key, entry := range entries {
		if !live[key] {
			result = append(result, entry)
		}
	}
	slices.SortFunc(result, func(left, right Entry) int {
		if order := right.UpdatedAt.Compare(left.UpdatedAt); order != 0 {
			return order
		}
		return strings.Compare(left.SessionID, right.SessionID)
	})
	return result, err
}

func Clear(root string) error {
	return scan(root, false, func(path string, _ Entry, active bool) error {
		if active {
			return nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func scan(root string, decode bool, visit func(string, Entry, bool) error) error {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	unlockRegistry, err := lockRegistry(root)
	if err != nil {
		return err
	}
	defer unlockRegistry()
	files, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("listing recovery records: %w", err)
	}
	var scanErrors []error
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		path := filepath.Join(root, file.Name())
		release, err := lock.TryFile(strings.TrimSuffix(path, ".json") + ".lock")
		active := errors.Is(err, lock.ErrContended)
		if err != nil && !active {
			scanErrors = append(scanErrors, fmt.Errorf("checking recovery record %s: %w", file.Name(), err))
			continue
		}
		err = func() error {
			if release != nil {
				defer release()
			}
			if !decode {
				return visit(path, Entry{}, active)
			}
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			var entry Entry
			if err := json.Unmarshal(data, &entry); err != nil {
				return fmt.Errorf("decoding recovery record %s: %w", file.Name(), err)
			}
			if entry.SessionID == "" {
				return nil
			}
			return visit(path, entry, active)
		}()
		if err != nil {
			scanErrors = append(scanErrors, err)
		}
	}
	if !decode {
		scanErrors = append(scanErrors, sweepArtifacts(root))
	}
	return errors.Join(scanErrors...)
}

func lockRegistry(root string) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return lock.File(ctx, filepath.Join(root, ".registry.lock"))
}

func sweepArtifacts(root string) error {
	files, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	active := make(map[string]bool)
	var cleanupErrors []error
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".lock") || file.Name() == ".registry.lock" {
			continue
		}
		owner := strings.TrimSuffix(file.Name(), ".lock")
		path := filepath.Join(root, file.Name())
		release, err := lock.TryFile(path)
		if errors.Is(err, lock.ErrContended) {
			active[owner] = true
			continue
		}
		if err != nil {
			active[owner] = true
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		release()
		if _, err := os.Stat(filepath.Join(root, owner+".json")); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		owner, _, ownedTemp := strings.Cut(file.Name(), ".tmp-")
		legacy := strings.HasPrefix(file.Name(), ".recovery-")
		if !ownedTemp && !legacy {
			continue
		}
		if ownedTemp && active[owner] {
			continue
		}
		if legacy {
			info, err := file.Info()
			if err != nil {
				cleanupErrors = append(cleanupErrors, err)
				continue
			}
			if len(active) > 0 || time.Since(info.ModTime()) < 24*time.Hour {
				continue
			}
		}
		if err := os.Remove(filepath.Join(root, file.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

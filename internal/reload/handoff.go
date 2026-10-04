package reload

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	maxHandoffSize   = 64 << 10
	handoffRetention = 7 * 24 * time.Hour // Sweep only; never gates Load.
)

// Handoff is the state a reloading process passes to its replacement.
type Handoff struct {
	SessionID   string    `json:"session_id"`
	Draft       string    `json:"draft"`
	YoloLevel   string    `json:"yolo_level"`
	BouncerMode string    `json:"bouncer_mode"`
	FromVersion string    `json:"from_version"`
	CreatedAt   time.Time `json:"created_at"`
}

// Dir returns the directory that holds handoff files for dataDir.
func Dir(dataDir string) string {
	return filepath.Join(dataDir, "reload")
}

// Write stores h in a new 0o600 file under dir, creating dir with mode
// 0o700 if needed, and returns the file's path.
func Write(dir string, h Handoff) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create handoff dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve handoff dir: %w", err)
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now()
	}
	data, err := json.Marshal(h)
	if err != nil {
		return "", fmt.Errorf("encode handoff: %w", err)
	}
	if len(data) > maxHandoffSize {
		return "", fmt.Errorf("handoff is %d bytes, over the %d byte limit", len(data), maxHandoffSize)
	}
	f, err := os.CreateTemp(resolved, "handoff-*.json")
	if err != nil {
		return "", fmt.Errorf("create handoff file: %w", err)
	}
	path := f.Name()
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("chmod handoff file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write handoff file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close handoff file: %w", err)
	}
	return path, nil
}

// Load reads and validates the handoff at path. It doesn't delete the file.
// There is deliberately no age limit: a draft surviving a failed startup is
// the point, and a handoff only applies when the exec passes its path.
func Load(dir, path string) (Handoff, error) {
	return validate(dir, path)
}

// Remove deletes the handoff at path, but only if it passes the same checks
// as Load.
func Remove(dir, path string) error {
	if _, err := validate(dir, path); err != nil {
		return err
	}
	return os.Remove(filepath.Clean(path))
}

// Sweep removes regular files in dir last modified more than
// handoffRetention before now.
func Sweep(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= handoffRetention {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

func validate(dir, path string) (Handoff, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Handoff{}, fmt.Errorf("resolve handoff dir: %w", err)
	}
	path = filepath.Clean(path)
	if filepath.Dir(path) != resolved {
		return Handoff{}, fmt.Errorf("handoff %s is outside %s", path, resolved)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Handoff{}, err
	}
	if !info.Mode().IsRegular() {
		return Handoff{}, fmt.Errorf("handoff %s is not a regular file", path)
	}
	if err := checkOwner(info); err != nil {
		return Handoff{}, fmt.Errorf("handoff %s: %w", path, err)
	}
	if info.Size() > maxHandoffSize {
		return Handoff{}, fmt.Errorf("handoff %s is %d bytes, over the %d byte limit", path, info.Size(), maxHandoffSize)
	}

	f, err := os.Open(path)
	if err != nil {
		return Handoff{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return Handoff{}, err
	}
	if !os.SameFile(info, opened) {
		return Handoff{}, fmt.Errorf("handoff %s changed while opening", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHandoffSize+1))
	if err != nil {
		return Handoff{}, err
	}
	if len(data) > maxHandoffSize {
		return Handoff{}, fmt.Errorf("handoff %s is over the %d byte limit", path, maxHandoffSize)
	}
	var h Handoff
	if err := json.Unmarshal(data, &h); err != nil {
		return Handoff{}, fmt.Errorf("decode handoff %s: %w", path, err)
	}
	return h, nil
}

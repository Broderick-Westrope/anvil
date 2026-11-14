package secrets

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/lock"
)

const (
	keyFile        = "secrets.key"
	keySize        = 32
	keyLockTimeout = 5 * time.Second
)

// LoadKey returns the placeholder tag key stored in dir, creating it on
// first use and replacing it if it is malformed. One key per install gives
// every session, subagent and process the same placeholder for the same
// value, which keeps prompt caches warm. Instances starting together take
// a file lock, so they agree on one key, and the key is renamed into place,
// so no reader sees a partly written file.
func LoadKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, keyFile)
	locks := filepath.Join(dir, "locks")
	err := os.MkdirAll(locks, 0o700)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", locks, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyLockTimeout)
	defer cancel()
	unlock, err := lock.File(ctx, filepath.Join(locks, keyFile+".lock"))
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	defer unlock()

	key, err := os.ReadFile(path)
	switch {
	case err == nil && len(key) == keySize:
		return key, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	key = RandomKey()
	tmp, err := os.CreateTemp(dir, keyFile+".*")
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(key)
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return key, nil
}

// RandomKey returns a fresh tag key, for a process that can't store one.
func RandomKey() []byte {
	key := make([]byte, keySize)
	_, _ = rand.Read(key)
	return key
}

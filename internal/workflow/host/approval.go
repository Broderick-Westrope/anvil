package host

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// FileStatus is how a bundled file changed since the last approval.
type FileStatus string

const (
	FileAdded     FileStatus = "added"
	FileChanged   FileStatus = "changed"
	FileRemoved   FileStatus = "removed"
	FileUnchanged FileStatus = "unchanged"
)

// ApprovalFile is one file of the bundle the user approves.
type ApprovalFile struct {
	// Path is relative to the workflow script's directory.
	Path   string
	Source string
	// PreviousSource is the last approved version, empty for an added file.
	PreviousSource string
	Status         FileStatus
}

// bundleFile is a file approval covers, as stored with the approval.
type bundleFile struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}

// bundle is every file under a workflow script's directory, sorted by
// path. Approval covers all of them, not just the script, because a script
// loads the files it bundles (reference(), bash on a bundled script) and
// they can change after it was approved.
type bundle []bundleFile

// loadBundle reads every regular file under the script's directory,
// following symlinked files but not symlinked directories, and skipping
// dot directories and node_modules. The script's entry is its parsed
// source.
func loadBundle(script *workflow.Script) (bundle, error) {
	root := script.Dir()
	var b bundle
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil //nolint:nilerr // A dangling symlink or special file isn't bundled.
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if path == filepath.Clean(script.Path) {
			// The script as parsed, which is what runs, even if the file
			// changed since.
			b = append(b, bundleFile{Path: rel, Source: script.Source})
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b = append(b, bundleFile{Path: rel, Source: string(data)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read workflow bundle %s: %w", root, err)
	}
	slices.SortFunc(b, func(x, y bundleFile) int { return strings.Compare(x.Path, y.Path) })
	return b, nil
}

// hash identifies the bundle's exact paths and contents. Each field is
// length-prefixed so no two bundles encode the same.
func (b bundle) hash() string {
	h := sha256.New()
	field := func(s string) {
		_ = binary.Write(h, binary.BigEndian, uint64(len(s)))
		h.Write([]byte(s))
	}
	for _, f := range b {
		field(f.Path)
		field(f.Source)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (b bundle) encode() (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("encode workflow bundle: %w", err)
	}
	return string(data), nil
}

// decodeBundle reads a stored bundle. One that doesn't decode is treated
// as empty, so every file shows as added.
func decodeBundle(stored string) bundle {
	var b bundle
	if err := json.Unmarshal([]byte(stored), &b); err != nil {
		return nil
	}
	return b
}

// approvalFiles compares the current bundle with the last approved one:
// the script first, then every other current or removed file by path.
func approvalFiles(scriptPath string, current, previous bundle) []ApprovalFile {
	prev := make(map[string]string, len(previous))
	for _, f := range previous {
		prev[f.Path] = f.Source
	}
	files := make([]ApprovalFile, 0, len(current)+len(previous))
	seen := make(map[string]bool, len(current))
	for _, f := range current {
		seen[f.Path] = true
		file := ApprovalFile{Path: f.Path, Source: f.Source, Status: FileAdded}
		if old, ok := prev[f.Path]; ok {
			file.PreviousSource, file.Status = old, FileChanged
			if old == f.Source {
				file.Status = FileUnchanged
			}
		}
		files = append(files, file)
	}
	for _, f := range previous {
		if !seen[f.Path] {
			files = append(files, ApprovalFile{Path: f.Path, PreviousSource: f.Source, Status: FileRemoved})
		}
	}
	slices.SortStableFunc(files, func(x, y ApprovalFile) int {
		switch {
		case x.Path == scriptPath:
			return -1
		case y.Path == scriptPath:
			return 1
		default:
			return strings.Compare(x.Path, y.Path)
		}
	})
	return files
}

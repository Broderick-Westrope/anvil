package workflowhost

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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
	// Size and Hash (SHA-256, hex) are the file's, or for a removed file
	// the last approved version's.
	Size int64
	Hash string
	// Omitted says why Source and PreviousSource are empty: "binary" or
	// "over 256 KiB". Such a file is approved by its size and hash.
	Omitted string
}

// bundleFile is a file approval covers, as stored with the approval.
type bundleFile struct {
	Path string `json:"path"`
	// Source is empty when Omitted is set.
	Source  string `json:"source,omitempty"`
	Size    int64  `json:"size"`
	Hash    string `json:"hash"`
	Omitted string `json:"omitted,omitempty"`
}

const (
	// maxShownFileSize is the largest file whose content approval shows
	// and stores; a larger one is approved by its size and hash.
	maxShownFileSize = 256 * 1024
	// binarySniffSize is how much of a file is searched for a NUL byte to
	// tell that it's binary, as git does.
	binarySniffSize = 8 * 1024
)

// newBundleFile describes a file with content data. A binary or large
// file keeps only its size and hash: it can't be reviewed as text, and
// storing it with every approval would bloat the database.
func newBundleFile(path string, data []byte) bundleFile {
	sum := sha256.Sum256(data)
	f := bundleFile{Path: path, Size: int64(len(data)), Hash: hex.EncodeToString(sum[:])}
	if bytes.IndexByte(data[:min(len(data), binarySniffSize)], 0) >= 0 {
		f.Omitted = "binary"
		return f
	}
	if len(data) > maxShownFileSize {
		f.Omitted = "over 256 KiB"
		return f
	}
	f.Source = string(data)
	return f
}

// bundle is every file under a workflow script's directory, sorted by
// path. Approval covers all of them, not just the script, because a script
// loads the files it bundles (reference(), bash on a bundled script) and
// they can change after it was approved.
type bundle []bundleFile

// loadBundle reads the files bundled with the script, as
// [workflow.Script.Bundled] lists them. The script's entry is its parsed
// source.
func loadBundle(script *workflow.Script) (bundle, error) {
	files, err := script.Bundled()
	if err != nil {
		return nil, err
	}
	scriptRel := filepath.Base(script.Path)
	b := make(bundle, 0, len(files))
	for _, f := range files {
		if f.Rel == scriptRel {
			// The script as parsed, which is what runs, even if the file
			// changed since. It's always shown, however large, since it's
			// what runs.
			file := newBundleFile(f.Rel, []byte(script.Source))
			file.Source, file.Omitted = script.Source, ""
			b = append(b, file)
			continue
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, fmt.Errorf("read workflow bundle file %s: %w", f.Path, err)
		}
		b = append(b, newBundleFile(f.Rel, data))
	}
	return b, nil
}

// references are the bundle's text files by path, for reference() to
// serve. Omitted files aren't among them.
func (b bundle) references() map[string]string {
	refs := make(map[string]string, len(b))
	for _, f := range b {
		if f.Omitted == "" {
			refs[f.Path] = f.Source
		}
	}
	return refs
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
		field(f.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// approvalFiles compares the current bundle with the last approved one:
// the script first, then every other current or removed file by path.
func approvalFiles(scriptPath string, current, previous bundle) []ApprovalFile {
	prev := make(map[string]bundleFile, len(previous))
	for _, f := range previous {
		prev[f.Path] = f
	}
	files := make([]ApprovalFile, 0, len(current)+len(previous))
	seen := make(map[string]bool, len(current))
	for _, f := range current {
		seen[f.Path] = true
		file := ApprovalFile{
			Path: f.Path, Source: f.Source, Status: FileAdded,
			Size: f.Size, Hash: f.Hash, Omitted: f.Omitted,
		}
		if old, ok := prev[f.Path]; ok {
			file.PreviousSource, file.Status = old.Source, FileChanged
			if old.Hash == f.Hash {
				file.Status = FileUnchanged
			}
		}
		files = append(files, file)
	}
	for _, f := range previous {
		if !seen[f.Path] {
			files = append(files, ApprovalFile{
				Path: f.Path, PreviousSource: f.Source, Status: FileRemoved,
				Size: f.Size, Hash: f.Hash, Omitted: f.Omitted,
			})
		}
	}
	slices.SortStableFunc(files, func(x, y ApprovalFile) int {
		if x.Path == scriptPath {
			return -1
		}
		if y.Path == scriptPath {
			return 1
		}
		return strings.Compare(x.Path, y.Path)
	})
	return files
}

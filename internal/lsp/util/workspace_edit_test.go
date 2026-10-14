package util_test

import (
	"os"
	"path/filepath"
	"testing"

	powernap "github.com/charmbracelet/x/powernap/pkg/lsp"
	"github.com/charmbracelet/x/powernap/pkg/lsp/protocol"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/lsp/util"
)

const invalidURI = protocol.DocumentURI("http://example.com/file.go")

func applyChange(change protocol.DocumentChange) error {
	return util.ApplyWorkspaceEdit(protocol.WorkspaceEdit{
		DocumentChanges: []protocol.DocumentChange{change},
	}, powernap.UTF8)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func requireContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, string(got))
}

func TestApplyWorkspaceEdit_CreateFile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		existing *string
		options  *protocol.CreateFileOptions
		want     string
	}{
		"new file":                          {want: ""},
		"existing file without options":     {existing: new("old"), want: ""},
		"existing file with empty options":  {existing: new("old"), options: &protocol.CreateFileOptions{}, want: ""},
		"existing file with overwrite":      {existing: new("old"), options: &protocol.CreateFileOptions{Overwrite: true}, want: ""},
		"existing file with ignore":         {existing: new("old"), options: &protocol.CreateFileOptions{IgnoreIfExists: true}, want: "old"},
		"missing file with ignore":          {options: &protocol.CreateFileOptions{IgnoreIfExists: true}, want: ""},
		"overwrite takes precedence":        {existing: new("old"), options: &protocol.CreateFileOptions{Overwrite: true, IgnoreIfExists: true}, want: ""},
		"new file with overwrite and empty": {options: &protocol.CreateFileOptions{Overwrite: true}, want: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "f.txt")
			if tc.existing != nil {
				writeFile(t, path, *tc.existing)
			}

			err := applyChange(protocol.DocumentChange{CreateFile: &protocol.CreateFile{
				URI:     protocol.URIFromPath(path),
				Options: tc.options,
			}})

			require.NoError(t, err)
			requireContent(t, path, tc.want)
		})
	}
}

func TestApplyWorkspaceEdit_CreateFileErrors(t *testing.T) {
	t.Parallel()

	err := applyChange(protocol.DocumentChange{CreateFile: &protocol.CreateFile{URI: invalidURI}})
	require.ErrorContains(t, err, "failed to apply document change: invalid URI: ")

	missingDir := filepath.Join(t.TempDir(), "missing", "f.txt")
	err = applyChange(protocol.DocumentChange{CreateFile: &protocol.CreateFile{URI: protocol.URIFromPath(missingDir)}})
	require.ErrorContains(t, err, "failed to apply document change: failed to create file: ")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestApplyWorkspaceEdit_DeleteFile(t *testing.T) {
	t.Parallel()

	t.Run("file", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "f.txt")
		writeFile(t, path, "x")

		err := applyChange(protocol.DocumentChange{DeleteFile: &protocol.DeleteFile{URI: protocol.URIFromPath(path)}})

		require.NoError(t, err)
		require.NoFileExists(t, path)
	})

	t.Run("non-empty directory without recursive fails", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "d")
		require.NoError(t, os.Mkdir(dir, 0o755))
		writeFile(t, filepath.Join(dir, "f.txt"), "x")

		err := applyChange(protocol.DocumentChange{DeleteFile: &protocol.DeleteFile{
			URI:     protocol.URIFromPath(dir),
			Options: &protocol.DeleteFileOptions{},
		}})

		require.ErrorContains(t, err, "failed to apply document change: failed to delete file: ")
		require.DirExists(t, dir)
	})

	t.Run("non-empty directory with recursive", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "d")
		require.NoError(t, os.Mkdir(dir, 0o755))
		writeFile(t, filepath.Join(dir, "f.txt"), "x")

		err := applyChange(protocol.DocumentChange{DeleteFile: &protocol.DeleteFile{
			URI:     protocol.URIFromPath(dir),
			Options: &protocol.DeleteFileOptions{Recursive: true},
		}})

		require.NoError(t, err)
		require.NoDirExists(t, dir)
	})

	t.Run("missing file fails", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "missing.txt")

		err := applyChange(protocol.DocumentChange{DeleteFile: &protocol.DeleteFile{URI: protocol.URIFromPath(path)}})

		require.ErrorContains(t, err, "failed to apply document change: failed to delete file: ")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("invalid URI", func(t *testing.T) {
		t.Parallel()

		err := applyChange(protocol.DocumentChange{DeleteFile: &protocol.DeleteFile{URI: invalidURI}})

		require.ErrorContains(t, err, "failed to apply document change: invalid URI: ")
	})
}

func TestApplyWorkspaceEdit_RenameFile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		targetExists bool
		options      *protocol.RenameFileOptions
		wantErr      string
	}{
		"to new path":                     {},
		"over existing without options":   {targetExists: true},
		"over existing with overwrite":    {targetExists: true, options: &protocol.RenameFileOptions{Overwrite: true}},
		"to new path without overwrite":   {options: &protocol.RenameFileOptions{}},
		"over existing without overwrite": {targetExists: true, options: &protocol.RenameFileOptions{}, wantErr: "target file already exists and overwrite is not allowed: "},
		"over existing with ignore only":  {targetExists: true, options: &protocol.RenameFileOptions{IgnoreIfExists: true}, wantErr: "target file already exists and overwrite is not allowed: "},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			oldPath := filepath.Join(dir, "old.txt")
			newPath := filepath.Join(dir, "new.txt")
			writeFile(t, oldPath, "moved")
			if tc.targetExists {
				writeFile(t, newPath, "existing")
			}

			err := applyChange(protocol.DocumentChange{RenameFile: &protocol.RenameFile{
				OldURI:  protocol.URIFromPath(oldPath),
				NewURI:  protocol.URIFromPath(newPath),
				Options: tc.options,
			}})

			if tc.wantErr != "" {
				require.EqualError(t, err, "failed to apply document change: "+tc.wantErr+newPath)
				requireContent(t, oldPath, "moved")
				requireContent(t, newPath, "existing")
				return
			}
			require.NoError(t, err)
			require.NoFileExists(t, oldPath)
			requireContent(t, newPath, "moved")
		})
	}
}

func TestApplyWorkspaceEdit_RenameFileErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	valid := protocol.URIFromPath(filepath.Join(dir, "a.txt"))

	err := applyChange(protocol.DocumentChange{RenameFile: &protocol.RenameFile{OldURI: invalidURI, NewURI: valid}})
	require.ErrorContains(t, err, "failed to apply document change: invalid URI ")

	err = applyChange(protocol.DocumentChange{RenameFile: &protocol.RenameFile{OldURI: valid, NewURI: invalidURI}})
	require.ErrorContains(t, err, "failed to apply document change: invalid URI ")

	err = applyChange(protocol.DocumentChange{RenameFile: &protocol.RenameFile{
		OldURI: valid,
		NewURI: protocol.URIFromPath(filepath.Join(dir, "b.txt")),
	}})
	require.ErrorContains(t, err, "failed to apply document change: failed to rename file: ")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestApplyWorkspaceEdit_TextDocumentEdit(t *testing.T) {
	t.Parallel()

	edit := func(startChar, endChar uint32, text string) protocol.TextEdit {
		return protocol.TextEdit{
			Range: protocol.Range{
				Start: protocol.Position{Line: 0, Character: startChar},
				End:   protocol.Position{Line: 0, Character: endChar},
			},
			NewText: text,
		}
	}

	t.Run("applies plain and annotated edits", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "f.txt")
		writeFile(t, path, "hello world\n")

		err := applyChange(protocol.DocumentChange{TextDocumentEdit: &protocol.TextDocumentEdit{
			TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: protocol.URIFromPath(path)},
			},
			Edits: []protocol.Or_TextDocumentEdit_edits_Elem{
				{Value: edit(0, 5, "howdy")},
				{Value: protocol.AnnotatedTextEdit{TextEdit: edit(6, 11, "there")}},
			},
		}})

		require.NoError(t, err)
		requireContent(t, path, "howdy there\n")
	})

	t.Run("rejects an unknown edit type", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "f.txt")
		writeFile(t, path, "hello\n")

		err := applyChange(protocol.DocumentChange{TextDocumentEdit: &protocol.TextDocumentEdit{
			TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: protocol.URIFromPath(path)},
			},
			Edits: []protocol.Or_TextDocumentEdit_edits_Elem{{Value: edit(0, 1, "j")}, {}},
		}})

		require.EqualError(t, err, "failed to apply document change: invalid edit type: nil text edit")
		requireContent(t, path, "hello\n")
	})
}

func TestApplyWorkspaceEdit_EmptyChange(t *testing.T) {
	t.Parallel()

	require.NoError(t, applyChange(protocol.DocumentChange{}))
}

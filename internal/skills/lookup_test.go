package skills

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookup(t *testing.T) {
	t.Run("exact match on disk skill with absolute path", func(t *testing.T) {
		skillPath := filepath.Join(t.TempDir(), "euc-go", "SKILL.md")
		registry := []*Skill{
			{Name: "euc-go", Description: "d", SkillFilePath: skillPath},
		}
		got, err := Lookup(registry, "euc-go")
		require.NoError(t, err)
		require.Equal(t, skillPath, got.Location)
		require.False(t, got.Builtin)
		require.Same(t, registry[0], got.Skill)
	})

	t.Run("relative SkillFilePath resolves against process working directory", func(t *testing.T) {
		dirA := t.TempDir()
		registry := []*Skill{
			{Name: "euc-go", Description: "d", SkillFilePath: "euc-go/SKILL.md"},
		}

		t.Chdir(dirA)
		gotA, err := Lookup(registry, "euc-go")
		require.NoError(t, err)
		require.Equal(t, filepath.Join(dirA, "euc-go/SKILL.md"), gotA.Location)

		dirB := t.TempDir()
		t.Chdir(dirB)
		gotB, err := Lookup(registry, "euc-go")
		require.NoError(t, err)
		require.Equal(t, filepath.Join(dirB, "euc-go/SKILL.md"), gotB.Location)

		require.NotEqual(t, gotA.Location, gotB.Location)
	})

	t.Run("builtin winner yields SkillFilePath verbatim", func(t *testing.T) {
		registry := []*Skill{
			{Name: "jq", Description: "d", SkillFilePath: BuiltinPrefix + "jq/SKILL.md", Source: SourceBuiltin},
		}
		got, err := Lookup(registry, "jq")
		require.NoError(t, err)
		require.Equal(t, BuiltinPrefix+"jq/SKILL.md", got.Location)
		require.True(t, got.Builtin)
	})

	t.Run("case mismatch returns ErrNotInRegistry", func(t *testing.T) {
		registry := []*Skill{
			{Name: "euc-go", Description: "d", SkillFilePath: "/abs/euc-go/SKILL.md"},
		}
		_, err := Lookup(registry, "Euc-Go")
		require.ErrorIs(t, err, ErrNotInRegistry)
	})

	t.Run("empty name returns ErrNotInRegistry", func(t *testing.T) {
		registry := []*Skill{
			{Name: "euc-go", Description: "d", SkillFilePath: "/abs/euc-go/SKILL.md"},
		}
		_, err := Lookup(registry, "")
		require.ErrorIs(t, err, ErrNotInRegistry)
	})

	t.Run("user entry shadows builtin in an already-deduplicated registry", func(t *testing.T) {
		skillPath := filepath.Join(t.TempDir(), "user-jq", "SKILL.md")
		registry := Deduplicate([]*Skill{
			{Name: "jq", Description: "builtin", SkillFilePath: BuiltinPrefix + "jq/SKILL.md", Source: SourceBuiltin},
			{Name: "jq", Description: "user", SkillFilePath: skillPath},
		})
		got, err := Lookup(registry, "jq")
		require.NoError(t, err)
		require.Equal(t, skillPath, got.Location)
		require.False(t, got.Builtin)
	})

	t.Run("entry with empty SkillFilePath returns ErrNoLocation", func(t *testing.T) {
		registry := []*Skill{
			{Name: "broken", Description: "d", SkillFilePath: ""},
		}
		_, err := Lookup(registry, "broken")
		require.ErrorIs(t, err, ErrNoLocation)
	})

	t.Run("symlinked skill directory keeps the symlink in Location", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks require elevated privileges on Windows")
		}
		real := t.TempDir()
		realSkillDir := filepath.Join(real, "euc-go")
		require.NoError(t, os.MkdirAll(realSkillDir, 0o755))
		realFile := filepath.Join(realSkillDir, "SKILL.md")
		require.NoError(t, os.WriteFile(realFile, []byte("---\nname: euc-go\ndescription: d\n---\nbody"), 0o644))

		linkParent := t.TempDir()
		linkDir := filepath.Join(linkParent, "euc-go-link")
		require.NoError(t, os.Symlink(realSkillDir, linkDir))
		linkFile := filepath.Join(linkDir, "SKILL.md")

		registry := []*Skill{
			{Name: "euc-go", Description: "d", SkillFilePath: linkFile},
		}
		got, err := Lookup(registry, "euc-go")
		require.NoError(t, err)
		require.Equal(t, linkFile, got.Location)
	})
}

func TestLocatedBaseDir(t *testing.T) {
	t.Run("builtin URI splits the prefix before applying path.Dir", func(t *testing.T) {
		l := Located{Location: BuiltinPrefix + "jq/SKILL.md", Builtin: true}
		require.Equal(t, "anvil://skills/jq", l.BaseDir())
	})

	t.Run("disk skill uses filepath.Dir", func(t *testing.T) {
		l := Located{Location: filepath.Join("abs", "euc-go", "SKILL.md")}
		require.Equal(t, filepath.Join("abs", "euc-go"), l.BaseDir())
	})
}

func TestLookupErrorsAreDistinct(t *testing.T) {
	require.False(t, errors.Is(ErrNotInRegistry, ErrNoLocation))
}

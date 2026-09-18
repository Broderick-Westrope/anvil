package skills

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

// ErrNotInRegistry is returned when no enabled skill in the snapshot has
// the requested name.
var ErrNotInRegistry = errors.New("skill not in registry snapshot")

// ErrNoLocation is returned when the winning registry entry has no source
// file path recorded, which means discovery produced an unusable entry.
var ErrNoLocation = errors.New("skill has no recorded location")

// Located is a resolved skill plus the canonical location of the source
// that won name precedence.
type Located struct {
	Skill *Skill
	// Location is an absolute filesystem path, or the verbatim
	// anvil://skills/<name>/SKILL.md URI for builtin winners. Symlinks are
	// deliberately not resolved so that relative asset references keep the
	// base directory the skill author intended.
	Location string
	Builtin  bool
}

// BaseDir returns the directory that relative references inside the skill
// resolve against. For builtin winners it splits BuiltinPrefix off first and
// applies path.Dir only to the suffix, because path.Dir on the whole URI
// collapses the "//" and yields anvil:/skills/<name>.
func (l Located) BaseDir() string {
	if l.Builtin {
		return BuiltinPrefix + path.Dir(strings.TrimPrefix(l.Location, BuiltinPrefix))
	}
	return filepath.Dir(l.Location)
}

// Lookup finds the enabled skill with exactly this name (case sensitive) in
// an already deduplicated and disabled-filtered registry snapshot. It never
// touches the filesystem and never searches.
func Lookup(registry []*Skill, name string) (Located, error) {
	if name == "" {
		return Located{}, ErrNotInRegistry
	}

	for _, s := range registry {
		if s.Name != name {
			continue
		}
		if s.SkillFilePath == "" {
			return Located{}, ErrNoLocation
		}
		if strings.HasPrefix(s.SkillFilePath, BuiltinPrefix) {
			return Located{Skill: s, Location: s.SkillFilePath, Builtin: true}, nil
		}
		// filepath.Abs resolves against the process working directory,
		// matching what discovery walked; never join against a tool's
		// workingDir. EvalSymlinks is deliberately not called so a
		// symlinked skill directory keeps relative asset references
		// pointed at the base directory the skill author intended.
		abs, err := filepath.Abs(s.SkillFilePath)
		if err != nil {
			return Located{}, err
		}
		return Located{Skill: s, Location: abs}, nil
	}

	return Located{}, ErrNotInRegistry
}

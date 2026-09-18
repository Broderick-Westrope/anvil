package skills

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

var ErrNotInRegistry = errors.New("skill not in registry snapshot")

var ErrNoLocation = errors.New("skill has no recorded location")

type Located struct {
	Skill    *Skill
	Location string
	Builtin  bool
}

func (l Located) BaseDir() string {
	if l.Builtin {
		return BuiltinPrefix + path.Dir(strings.TrimPrefix(l.Location, BuiltinPrefix))
	}
	return filepath.Dir(l.Location)
}

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
		abs, err := filepath.Abs(s.SkillFilePath)
		if err != nil {
			return Located{}, err
		}
		return Located{Skill: s, Location: abs}, nil
	}

	return Located{}, ErrNotInRegistry
}

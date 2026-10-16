// Package workflow runs JavaScript workflow scripts that back commands.
// A script owns control flow and context boundaries; every side effect goes
// through a primitive implemented by a Backend, so it can be shown, logged
// and later replayed.
package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// Requirements are the skills, agents and references a script declares in
// its top-level requires() call.
type Requirements struct {
	Skills     []string `json:"skills,omitempty"`
	Agents     []string `json:"agents,omitempty"`
	References []string `json:"references,omitempty"`
}

// Script is a parsed workflow script. Parsing never executes any of the
// script's code.
type Script struct {
	Path     string
	Source   string
	Hash     string
	Requires Requirements
	// Problems are faults in the script's shape, such as a requires()
	// call that isn't a literal, that leave its requirements unknown. Parse
	// still returns the script so they can be shown, but a script with
	// problems must not run, and checking it fails.
	Problems []string
}

// Dir is the directory references resolve against.
func (s *Script) Dir() string { return filepath.Dir(s.Path) }

// Load reads and parses the script at path.
func Load(path string) (*Script, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow %s: %w", path, err)
	}
	return Parse(path, src)
}

// Parse parses src and reads its requires() call from the AST. It fails if
// the source doesn't parse or doesn't declare a top-level main function.
func Parse(path string, src []byte) (*Script, error) {
	program, err := parser.ParseFile(nil, path, string(src), 0)
	if err != nil {
		return nil, fmt.Errorf("parse workflow %s: %w", path, err)
	}
	sum := sha256.Sum256(src)
	script := &Script{Path: path, Source: string(src), Hash: hex.EncodeToString(sum[:])}

	hasMain, requiresCalls := false, 0
	for _, stmt := range program.Body {
		if fn, ok := stmt.(*ast.FunctionDeclaration); ok && fn.Function.Name != nil && fn.Function.Name.Name == "main" {
			hasMain = true
			continue
		}
		expr, ok := stmt.(*ast.ExpressionStatement)
		if !ok {
			continue
		}
		call, ok := expr.Expression.(*ast.CallExpression)
		if !ok {
			continue
		}
		if callee, ok := call.Callee.(*ast.Identifier); !ok || callee.Name != "requires" {
			continue
		}
		requiresCalls++
		if requiresCalls > 1 {
			script.Problems = append(script.Problems, "requires() must be called once")
			continue
		}
		reqs, err := literalRequirements(call)
		if err != nil {
			script.Problems = append(script.Problems, "requires() must be a literal: "+err.Error())
			continue
		}
		script.Requires = reqs
	}
	if !hasMain {
		return nil, fmt.Errorf("workflow %s has no top-level function main(input)", path)
	}
	return script, nil
}

func literalRequirements(call *ast.CallExpression) (Requirements, error) {
	var reqs Requirements
	if len(call.ArgumentList) != 1 {
		return reqs, fmt.Errorf("expected one object argument")
	}
	obj, ok := call.ArgumentList[0].(*ast.ObjectLiteral)
	if !ok {
		return reqs, fmt.Errorf("argument is not an object literal")
	}
	for _, prop := range obj.Value {
		keyed, ok := prop.(*ast.PropertyKeyed)
		if !ok || keyed.Computed {
			return reqs, fmt.Errorf("properties must be plain keys")
		}
		key, err := propertyKey(keyed.Key)
		if err != nil {
			return reqs, err
		}
		values, err := stringArray(keyed.Value)
		if err != nil {
			return reqs, fmt.Errorf("%s: %w", key, err)
		}
		switch key {
		case "skills":
			reqs.Skills = values
		case "agents":
			reqs.Agents = values
		case "references":
			reqs.References = values
		default:
			return reqs, fmt.Errorf("unknown key %q", key)
		}
	}
	return reqs, nil
}

func propertyKey(expr ast.Expression) (string, error) {
	switch key := expr.(type) {
	case *ast.Identifier:
		return key.Name.String(), nil
	case *ast.StringLiteral:
		return key.Value.String(), nil
	default:
		return "", fmt.Errorf("properties must be plain keys")
	}
}

func stringArray(expr ast.Expression) ([]string, error) {
	arr, ok := expr.(*ast.ArrayLiteral)
	if !ok {
		return nil, fmt.Errorf("expected an array of string literals")
	}
	values := make([]string, 0, len(arr.Value))
	for _, item := range arr.Value {
		str, ok := item.(*ast.StringLiteral)
		if !ok {
			return nil, fmt.Errorf("expected an array of string literals")
		}
		values = append(values, str.Value.String())
	}
	return values, nil
}

// Catalog answers whether a skill or agent exists, for checking a script's
// requirements.
type Catalog interface {
	HasSkill(name string) bool
	HasAgent(name string) bool
}

// Missing lists every declared requirement that doesn't resolve. A
// workflow with missing requirements must not run.
func (s *Script) Missing(cat Catalog) []string {
	var missing []string
	for _, name := range s.Requires.Skills {
		if !cat.HasSkill(name) {
			missing = append(missing, fmt.Sprintf("skill %q not found", name))
		}
	}
	for _, name := range s.Requires.Agents {
		if !cat.HasAgent(name) {
			missing = append(missing, fmt.Sprintf("agent %q not found", name))
		}
	}
	if len(s.Requires.References) == 0 {
		return missing
	}
	bundled := map[string]bool{}
	files, err := s.Bundled()
	if err != nil {
		return append(missing, err.Error())
	}
	for _, f := range files {
		bundled[f.Rel] = true
	}
	for _, ref := range s.Requires.References {
		key, err := referenceKey(ref)
		if err != nil {
			missing = append(missing, err.Error())
			continue
		}
		if !bundled[key] {
			missing = append(missing, fmt.Sprintf("reference %q not found", ref))
		}
	}
	return missing
}

// referenceKey is the bundled path, as in [BundledFile.Rel], a reference
// names. It refuses paths that escape the workflow directory.
func referenceKey(ref string) (string, error) {
	if filepath.IsAbs(ref) || !filepath.IsLocal(ref) {
		return "", fmt.Errorf("reference %q is outside the workflow directory", ref)
	}
	return filepath.Clean(filepath.FromSlash(ref)), nil
}

// BundledFile is a file bundled with a script.
type BundledFile struct {
	// Rel is the file's path relative to the script's directory.
	Rel string
	// Path is where to read it, with every symlink resolved.
	Path string
}

// Bundled lists the files bundled with the script, sorted by Rel: every
// regular file under its directory, which may itself be a symlink,
// skipping dot directories and node_modules. Symlinked files are followed
// when they resolve inside the directory; symlinked directories, and
// symlinks that resolve outside it, are left out. It fails if the script
// itself isn't bundled, so approval never covers a bundle that misses what
// runs.
func (s *Script) Bundled() ([]BundledFile, error) {
	root, err := filepath.EvalSymlinks(s.Dir())
	if err != nil {
		return nil, fmt.Errorf("resolve workflow directory %s: %w", s.Dir(), err)
	}
	scriptRel := filepath.Base(s.Path)
	var files []BundledFile
	hasScript := false
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			// A dangling symlink isn't bundled.
			return nil
		}
		if within, relErr := filepath.Rel(root, real); relErr != nil || !filepath.IsLocal(within) {
			return nil
		}
		// A symlinked directory or a special file isn't bundled.
		if info, statErr := os.Stat(real); statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hasScript = hasScript || rel == scriptRel
		files = append(files, BundledFile{Rel: rel, Path: real})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read workflow bundle %s: %w", root, err)
	}
	if !hasScript {
		return nil, fmt.Errorf("read workflow bundle %s: it doesn't contain the script %s", root, s.Path)
	}
	slices.SortFunc(files, func(x, y BundledFile) int { return strings.Compare(x.Rel, y.Rel) })
	return files, nil
}

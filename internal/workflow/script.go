// Package workflow runs JavaScript workflow scripts that back commands.
// A script owns control flow and context boundaries; every side effect goes
// through a primitive implemented by a Backend, so it can be shown, logged
// and later replayed.
package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

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
	// Warnings are problems with the script's shape that don't stop it from
	// running, such as a requires() call that isn't a literal.
	Warnings []string
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
		switch stmt := stmt.(type) {
		case *ast.FunctionDeclaration:
			if stmt.Function.Name != nil && stmt.Function.Name.Name == "main" {
				hasMain = true
			}
		case *ast.ExpressionStatement:
			call, ok := stmt.Expression.(*ast.CallExpression)
			if !ok {
				continue
			}
			if callee, ok := call.Callee.(*ast.Identifier); !ok || callee.Name != "requires" {
				continue
			}
			requiresCalls++
			if requiresCalls > 1 {
				script.Warnings = append(script.Warnings, "requires() must be called once")
				continue
			}
			reqs, err := literalRequirements(call)
			if err != nil {
				script.Warnings = append(script.Warnings, "requires() must be a literal: "+err.Error())
				continue
			}
			script.Requires = reqs
		}
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
	for _, ref := range s.Requires.References {
		path, err := s.referencePath(ref)
		if err != nil {
			missing = append(missing, err.Error())
			continue
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			missing = append(missing, fmt.Sprintf("reference %q not found", ref))
		}
	}
	return missing
}

// referencePath resolves ref against the workflow directory, refusing
// paths that escape it.
func (s *Script) referencePath(ref string) (string, error) {
	if filepath.IsAbs(ref) || !filepath.IsLocal(ref) {
		return "", fmt.Errorf("reference %q is outside the workflow directory", ref)
	}
	return filepath.Join(s.Dir(), ref), nil
}

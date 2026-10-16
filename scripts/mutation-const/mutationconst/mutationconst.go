// Package mutationconst marks gremlins survivors that sit where Go's
// coverage tool never instruments code, so no test can ever reach them.
//
// Go's coverage only instruments statements inside function bodies, and
// its blocks start after a case clause's colon. So gremlins reports every
// mutant in a package-level const declaration, or in the expressions of a
// switch case list, as NOT COVERED and never runs a test against it,
// however well the code is tested. scripts/mutation-check.sh uses this to
// set those aside.
//
// Run reads gremlins survivors as tab-separated lines (file, line, column,
// status, mutation) and writes each line back prefixed with "const",
// "case" or "keep" and a tab. Only NOT COVERED mutants can be set aside;
// anything it cannot parse is kept, so the gate stays strict.
//
// The logic lives outside package main because gremlins finds a mutant's
// package by matching the directory name to the package name, which never
// matches for a main package in a named directory.
package mutationconst

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"strconv"
	"strings"
)

// Run classifies survivors from in, writes them to out, and returns the
// process exit code, reporting any error to errOut.
func Run(in io.Reader, out, errOut io.Writer) int {
	if err := classify(in, out, regionsOf); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

// pos is a 1-based line and column, as gremlins reports them.
type pos struct{ line, col int }

func (p pos) after(q pos) bool {
	return p.line > q.line || (p.line == q.line && p.col > q.col)
}

// region is source Go coverage never instruments, labelled with why. from
// and to are inclusive.
type region struct {
	label    string
	from, to pos
}

// regionsFunc returns the uninstrumented regions of a file.
type regionsFunc func(path string) ([]region, error)

func classify(in io.Reader, out io.Writer, regions regionsFunc) error {
	cache := map[string][]region{}
	failed := map[string]bool{}
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		label := labelFor(line, cache, failed, regions)
		if _, err := fmt.Fprintf(out, "%s\t%s\n", label, line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func labelFor(line string, cache map[string][]region, failed map[string]bool, regions regionsFunc) string {
	fields := strings.Split(line, "\t")
	if len(fields) < 4 || fields[3] != "NOT COVERED" {
		return "keep"
	}
	lineNo, err := strconv.Atoi(fields[1])
	if err != nil {
		return "keep"
	}
	col, err := strconv.Atoi(fields[2])
	if err != nil {
		return "keep"
	}
	path := fields[0]
	if failed[path] {
		return "keep"
	}
	rs, ok := cache[path]
	if !ok {
		rs, err = regions(path)
		if err != nil {
			failed[path] = true
			return "keep"
		}
		cache[path] = rs
	}
	at := pos{lineNo, col}
	for _, r := range rs {
		if !r.from.after(at) && !at.after(r.to) {
			return r.label
		}
	}
	return "keep"
}

// regionsOf finds the regions of path that Go coverage never instruments:
// package-level const declarations (labelled "const", whole lines), and
// the expressions in switch case lists (labelled "case"), which sit
// between the case keyword and the colon, outside every coverage block.
func regionsOf(path string) ([]region, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	at := func(p token.Pos) pos {
		position := fset.Position(p)
		return pos{position.Line, position.Column}
	}
	var rs []region
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		rs = append(rs, region{
			label: "const",
			from:  pos{at(gen.Pos()).line, 0},
			to:    pos{at(gen.End()).line, math.MaxInt},
		})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range clause.List {
			end := at(expr.End())
			end.col-- // End is just past the expression.
			rs = append(rs, region{label: "case", from: at(expr.Pos()), to: end})
		}
		return true
	})
	return rs, nil
}

// Package mutationconst marks gremlins survivors that sit on package-level
// constant declarations.
//
// Go's coverage tool only instruments statements inside function bodies,
// so gremlins reports every mutant in a package-level const declaration as
// NOT COVERED and never runs a test against it, however well the value is
// tested. scripts/mutation-check.sh uses this to set those aside.
//
// Run reads gremlins survivors as tab-separated lines (file, line, column,
// status, mutation) and writes each line back prefixed with "const" or
// "keep" and a tab. Only NOT COVERED mutants can be marked "const";
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
	"strconv"
	"strings"
)

// Run classifies survivors from in, writes them to out, and returns the
// process exit code, reporting any error to errOut.
func Run(in io.Reader, out, errOut io.Writer) int {
	if err := classify(in, out, constRanges); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

// lineRange is an inclusive range of source lines.
type lineRange struct{ start, end int }

// rangesFunc returns the line ranges of package-level const declarations
// in a file.
type rangesFunc func(path string) ([]lineRange, error)

func classify(in io.Reader, out io.Writer, ranges rangesFunc) error {
	cache := map[string][]lineRange{}
	failed := map[string]bool{}
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		label := "keep"
		if isConst(line, cache, failed, ranges) {
			label = "const"
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\n", label, line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func isConst(line string, cache map[string][]lineRange, failed map[string]bool, ranges rangesFunc) bool {
	fields := strings.Split(line, "\t")
	if len(fields) < 4 || fields[3] != "NOT COVERED" {
		return false
	}
	lineNo, err := strconv.Atoi(fields[1])
	if err != nil {
		return false
	}
	path := fields[0]
	if failed[path] {
		return false
	}
	rs, ok := cache[path]
	if !ok {
		rs, err = ranges(path)
		if err != nil {
			failed[path] = true
			return false
		}
		cache[path] = rs
	}
	for _, r := range rs {
		if lineNo >= r.start && lineNo <= r.end {
			return true
		}
	}
	return false
}

func constRanges(path string) ([]lineRange, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var rs []lineRange
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		rs = append(rs, lineRange{
			start: fset.Position(gen.Pos()).Line,
			end:   fset.Position(gen.End()).Line,
		})
	}
	return rs, nil
}

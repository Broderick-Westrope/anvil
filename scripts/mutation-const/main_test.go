package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const source = `package sample

import "time"

const Single = 2 * time.Second

const (
	A = 1 << 2
	B = 3 * 4
)

var notConst = 5 * 6

func f() int {
	const local = 7 * 8
	return local
}
`

func writeSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.go")
	require.NoError(t, os.WriteFile(path, []byte(source), 0o644))
	return path
}

func TestConstRanges(t *testing.T) {
	t.Parallel()

	rs, err := constRanges(writeSource(t))
	require.NoError(t, err)
	require.Equal(t, []lineRange{{start: 5, end: 5}, {start: 7, end: 10}}, rs)

	_, err = constRanges(filepath.Join(t.TempDir(), "missing.go"))
	require.Error(t, err)
}

func TestClassify(t *testing.T) {
	t.Parallel()

	path := writeSource(t)
	in := strings.Join([]string{
		path + "\t5\t22\tNOT COVERED\tARITHMETIC_BASE",
		path + "\t7\t1\tNOT COVERED\tARITHMETIC_BASE",
		path + "\t9\t6\tNOT COVERED\tARITHMETIC_BASE",
		path + "\t10\t1\tNOT COVERED\tARITHMETIC_BASE",
		path + "\t11\t1\tNOT COVERED\tARITHMETIC_BASE",
		path + "\t12\t17\tNOT COVERED\tARITHMETIC_BASE",
		path + "\t15\t17\tNOT COVERED\tARITHMETIC_BASE",
		path + "\t5\t22\tLIVED\tARITHMETIC_BASE",
		path + "\tx\t1\tNOT COVERED\tARITHMETIC_BASE",
		path + "\t5",
		"",
	}, "\n")

	var out strings.Builder
	require.NoError(t, classify(strings.NewReader(in), &out, constRanges))

	var labels []string
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		label, rest, ok := strings.Cut(line, "\t")
		require.True(t, ok)
		require.True(t, strings.HasPrefix(rest, path+"\t"))
		labels = append(labels, label)
	}
	require.Equal(t, []string{
		"const", // Single-line const.
		"const", // Opening line of a const block.
		"const", // Inside the block.
		"const", // Closing paren.
		"keep",  // Blank line after the block.
		"keep",  // Package-level var.
		"keep",  // Function-local const is instrumented.
		"keep",  // LIVED means a test ran it.
		"keep",  // Unparseable line number.
		"keep",  // Too few fields.
	}, labels)
}

func TestClassifyKeepsUnparseableFiles(t *testing.T) {
	t.Parallel()

	calls := 0
	failing := func(string) ([]lineRange, error) {
		calls++
		return nil, errors.New("parse failed")
	}
	in := "a.go\t1\t1\tNOT COVERED\tX\na.go\t2\t1\tNOT COVERED\tX\n"

	var out strings.Builder
	require.NoError(t, classify(strings.NewReader(in), &out, failing))
	require.Equal(t, "keep\ta.go\t1\t1\tNOT COVERED\tX\nkeep\ta.go\t2\t1\tNOT COVERED\tX\n", out.String())
	require.Equal(t, 1, calls)
}

func TestClassifyCachesRanges(t *testing.T) {
	t.Parallel()

	calls := 0
	ranges := func(string) ([]lineRange, error) {
		calls++
		return []lineRange{{start: 3, end: 3}}, nil
	}
	in := "a.go\t3\t1\tNOT COVERED\tX\na.go\t4\t1\tNOT COVERED\tX\n"

	var out strings.Builder
	require.NoError(t, classify(strings.NewReader(in), &out, ranges))
	require.Equal(t, "const\ta.go\t3\t1\tNOT COVERED\tX\nkeep\ta.go\t4\t1\tNOT COVERED\tX\n", out.String())
	require.Equal(t, 1, calls)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestClassifyReturnsWriteErrors(t *testing.T) {
	t.Parallel()

	err := classify(strings.NewReader("a.go\t1\t1\tLIVED\tX\n"), failingWriter{}, constRanges)
	require.EqualError(t, err, "write failed")
}

package mutationconst

import (
	"errors"
	"math"
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

func TestRegionsOfConsts(t *testing.T) {
	t.Parallel()

	rs, err := regionsOf(writeSource(t))
	require.NoError(t, err)
	require.Equal(t, []region{
		{label: "const", from: pos{5, 0}, to: pos{5, math.MaxInt}},
		{label: "const", from: pos{7, 0}, to: pos{10, math.MaxInt}},
	}, rs)

	_, err = regionsOf(filepath.Join(t.TempDir(), "missing.go"))
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
	require.NoError(t, classify(strings.NewReader(in), &out, regionsOf))

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
	failing := func(string) ([]region, error) {
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
	ranges := func(string) ([]region, error) {
		calls++
		return []region{{label: "const", from: pos{3, 0}, to: pos{3, math.MaxInt}}}, nil
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

	err := classify(strings.NewReader("a.go\t1\t1\tLIVED\tX\n"), failingWriter{}, regionsOf)
	require.EqualError(t, err, "write failed")
}

func TestRun(t *testing.T) {
	t.Parallel()

	var out, errOut strings.Builder
	require.Equal(t, 0, Run(strings.NewReader("a.go\t1\t1\tLIVED\tX\n"), &out, &errOut))
	require.Equal(t, "keep\ta.go\t1\t1\tLIVED\tX\n", out.String())
	require.Empty(t, errOut.String())

	errOut.Reset()
	require.Equal(t, 1, Run(strings.NewReader("a.go\t1\t1\tLIVED\tX\n"), failingWriter{}, &errOut))
	require.Equal(t, "write failed\n", errOut.String())
}

func TestClassifySkipsBlankLinesAndNeedsOnlyStatus(t *testing.T) {
	t.Parallel()

	ranges := func(string) ([]region, error) {
		return []region{{label: "const", from: pos{3, 0}, to: pos{3, math.MaxInt}}}, nil
	}
	// A blank line must not stop processing, and the mutation type column
	// is optional: the status in the fourth field is enough.
	in := "a.go\t3\t1\tNOT COVERED\tX\n\na.go\t3\t1\tNOT COVERED\n"

	var out strings.Builder
	require.NoError(t, classify(strings.NewReader(in), &out, ranges))
	require.Equal(t, "const\ta.go\t3\t1\tNOT COVERED\tX\nconst\ta.go\t3\t1\tNOT COVERED\n", out.String())
}

const switchSource = `package sample

func g(a, b int) int {
	switch {
	case a < b, a+1 == b:
		return a - b
	}
	switch a * 2 {
	case b - 1:
		return 1
	}
	select {}
}
`

func TestClassifyCaseConditions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "switch.go")
	require.NoError(t, os.WriteFile(path, []byte(switchSource), 0o644))
	in := strings.Join([]string{
		path + "\t5\t9\tNOT COVERED\tCONDITIONALS_BOUNDARY", // Tagless case condition.
		path + "\t5\t16\tNOT COVERED\tARITHMETIC_BASE",      // Second expression in the same case.
		path + "\t6\t12\tNOT COVERED\tARITHMETIC_BASE",      // The case body is instrumented.
		path + "\t8\t11\tNOT COVERED\tARITHMETIC_BASE",      // A switch tag is instrumented.
		path + "\t9\t9\tNOT COVERED\tARITHMETIC_BASE",       // Tagged case value.
		path + "\t5\t9\tLIVED\tCONDITIONALS_BOUNDARY",       // LIVED means a test ran it.
		path + "\t5\t2\tNOT COVERED\tCONDITIONALS_BOUNDARY", // Before the expression, on the case keyword.
	}, "\n")

	var out strings.Builder
	require.NoError(t, classify(strings.NewReader(in), &out, regionsOf))

	var labels []string
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		label, _, ok := strings.Cut(line, "\t")
		require.True(t, ok)
		labels = append(labels, label)
	}
	require.Equal(t, []string{"case", "case", "keep", "keep", "case", "keep", "keep"}, labels)
}

package shell_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/shell"
)

type jqResult struct {
	stdout string
	stderr string
	code   int
}

func runJQ(t *testing.T, command, stdin string) jqResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := shell.Run(t.Context(), shell.RunOptions{
		Command: command,
		Cwd:     t.TempDir(),
		Stdin:   strings.NewReader(stdin),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	return jqResult{stdout: stdout.String(), stderr: stderr.String(), code: shell.ExitCode(err)}
}

func TestJQBuiltin(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		command string
		stdin   string
		want    jqResult
	}{
		"no filter defaults to identity and pretty prints": {
			command: `jq`,
			stdin:   `{"a":[1]}`,
			want:    jqResult{stdout: "{\n  \"a\": [\n    1\n  ]\n}\n"},
		},
		"compact long flag": {
			command: `jq --compact-output .`,
			stdin:   `{"a":[1]}`,
			want:    jqResult{stdout: "{\"a\":[1]}\n"},
		},
		"compact short flag": {
			command: `jq -c .`,
			stdin:   `{"a":[1]}`,
			want:    jqResult{stdout: "{\"a\":[1]}\n"},
		},
		"string output is quoted by default": {
			command: `jq .`,
			stdin:   `"x"`,
			want:    jqResult{stdout: "\"x\"\n"},
		},
		"raw output short flag": {
			command: `jq -r .`,
			stdin:   `"x" 1`,
			want:    jqResult{stdout: "x\n1\n"},
		},
		"raw output long flag": {
			command: `jq --raw-output .`,
			stdin:   `"x"`,
			want:    jqResult{stdout: "x\n"},
		},
		"join output drops newlines between strings": {
			command: `jq -j .`,
			stdin:   `"a" "b" 1`,
			want:    jqResult{stdout: "ab1\n"},
		},
		"join output long flag": {
			command: `jq --join-output .`,
			stdin:   `"a" "b"`,
			want:    jqResult{stdout: "ab"},
		},
		"slurp short flag": {
			command: `jq -c -s .`,
			stdin:   `1 2`,
			want:    jqResult{stdout: "[1,2]\n"},
		},
		"slurp long flag": {
			command: `jq -c --slurp .`,
			stdin:   `1 2`,
			want:    jqResult{stdout: "[1,2]\n"},
		},
		"null input ignores stdin": {
			command: `jq -n .`,
			stdin:   `1`,
			want:    jqResult{stdout: "null\n"},
		},
		"null input long flag": {
			command: `jq --null-input .`,
			stdin:   `1`,
			want:    jqResult{stdout: "null\n"},
		},
		"empty input runs the filter on null": {
			command: `jq .`,
			want:    jqResult{stdout: "null\n"},
		},
		"multiple inputs each run the filter": {
			command: `jq -c '.+1'`,
			stdin:   "1\n2\n",
			want:    jqResult{stdout: "2\n3\n"},
		},
		"exit status 1 when last value is false": {
			command: `jq -e .`,
			stdin:   `false`,
			want:    jqResult{stdout: "false\n", code: 1},
		},
		"exit status 1 when last value is null": {
			command: `jq --exit-status .`,
			stdin:   `null`,
			want:    jqResult{stdout: "null\n", code: 1},
		},
		"exit status 0 when only an earlier value is falsy": {
			command: `jq -e .`,
			stdin:   `false true`,
			want:    jqResult{stdout: "false\ntrue\n"},
		},
		"falsy value without exit status flag succeeds": {
			command: `jq .`,
			stdin:   `false`,
			want:    jqResult{stdout: "false\n"},
		},
		"raw input yields every line including empty ones": {
			command: `jq -c -R .`,
			stdin:   "a\n\nb\n",
			want:    jqResult{stdout: "\"a\"\n\"\"\n\"b\"\n\"\"\n"},
		},
		"raw input long flag": {
			command: `jq -c --raw-input .`,
			stdin:   "a",
			want:    jqResult{stdout: "\"a\"\n"},
		},
		"raw input with slurp yields the whole text": {
			command: `jq -c -R -s .`,
			stdin:   "a\n\nb\n",
			want:    jqResult{stdout: "\"a\\n\\nb\\n\"\n"},
		},
		"arg binds a string variable": {
			command: `jq -n -c '[$x, $y]' --arg x 1 --arg y two`,
			want:    jqResult{stdout: "[\"1\",\"two\"]\n"},
		},
		"argjson binds a JSON variable": {
			command: `jq -n -c '$x' --argjson x '{"a":1}'`,
			want:    jqResult{stdout: "{\"a\":1}\n"},
		},
		"arg before the filter": {
			command: `jq --arg x v -n '$x'`,
			want:    jqResult{stdout: "\"v\"\n"},
		},
		"arg missing value": {
			command: `jq -n . --arg x`,
			want:    jqResult{stderr: "jq: --arg requires name and value\n", code: 2},
		},
		"argjson missing value": {
			command: `jq -n . --argjson x`,
			want:    jqResult{stderr: "jq: --argjson requires name and value\n", code: 2},
		},
		"argjson invalid JSON": {
			command: `jq -n . --argjson x '{'`,
			want:    jqResult{stderr: "jq: invalid JSON for --argjson x: unexpected end of JSON input\n", code: 2},
		},
		"unknown option after the filter": {
			command: `jq . -x`,
			stdin:   `1`,
			want:    jqResult{stderr: "jq: unknown option: -x\n", code: 2},
		},
		"dash argument before the filter is the filter": {
			command: `jq -n -c '-1'`,
			want:    jqResult{stdout: "-1\n"},
		},
		"parse error": {
			command: `jq '.['`,
			stdin:   `1`,
			want:    jqResult{stderr: "jq: unexpected EOF\n", code: 3},
		},
		"compile error": {
			command: `jq 'nosuchfunc'`,
			stdin:   `1`,
			want:    jqResult{stderr: "jq: function not defined: nosuchfunc/0\n", code: 3},
		},
		"undefined variable is a compile error": {
			command: `jq -n '$x'`,
			want:    jqResult{stderr: "jq: variable not defined: $x\n", code: 3},
		},
		"invalid JSON input": {
			command: `jq .`,
			stdin:   `{`,
			want:    jqResult{stderr: "jq: parse error: unexpected EOF\n", code: 2},
		},
		"runtime error": {
			command: `jq 'error("boom")'`,
			stdin:   `1`,
			want:    jqResult{stderr: "jq: error: boom\n", code: 5},
		},
		"runtime error stops after earlier output": {
			command: `jq -c '1, error("boom"), 2'`,
			stdin:   `null`,
			want:    jqResult{stdout: "1\n", stderr: "jq: error: boom\n", code: 5},
		},
		"error before help is reported": {
			command: `jq . -x -h`,
			want:    jqResult{stderr: "jq: unknown option: -x\n", code: 2},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := runJQ(t, tc.command, tc.stdin)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestJQBuiltin_Help(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"short flag":                 `jq -h`,
		"long flag":                  `jq --help`,
		"help wins over later error": `jq -h --arg`,
		"help after other flags":     `jq -c . -h`,
	}
	for name, command := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := runJQ(t, command, "1")
			require.Equal(t, 0, got.code)
			require.Empty(t, got.stderr)
			require.True(t, strings.HasPrefix(got.stdout, "jq - Go implementation of jq"), "stdout = %q", got.stdout)
			require.Contains(t, got.stdout, "\n  % echo '{\"foo\": 128}' | jq '.foo'")
			require.True(t, strings.HasSuffix(got.stdout, "  -h, --help                    display this help\n"))
		})
	}
}

func TestJQBuiltin_Files(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")
	require.NoError(t, os.WriteFile(first, []byte(`1 2`), 0o644))
	require.NoError(t, os.WriteFile(second, []byte(`3`), 0o644))

	tests := map[string]struct {
		command string
		want    jqResult
	}{
		"files are read in order instead of stdin": {
			command: `jq -c . '` + first + `' '` + second + `'`,
			want:    jqResult{stdout: "1\n2\n3\n"},
		},
		"slurp collects each file separately": {
			command: `jq -c -s . '` + first + `' '` + second + `'`,
			want:    jqResult{stdout: "[1,2]\n[3]\n"},
		},
		"double dash makes the rest files": {
			command: `jq -c . -- '` + second + `'`,
			want:    jqResult{stdout: "3\n"},
		},
		"double dash before the filter makes everything files": {
			command: `jq -- '` + second + `'`,
			want:    jqResult{stdout: "3\n"},
		},
		"raw input reads each file": {
			command: `jq -c -R . '` + second + `'`,
			want:    jqResult{stdout: "\"3\"\n"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := runJQ(t, tc.command, "ignored")
			require.Equal(t, tc.want, got)
		})
	}
}

// The OS words file errors differently per platform, so these cases pin
// only the exit code and jq's own prefix.
func TestJQBuiltin_FileErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	require.NoError(t, os.WriteFile(first, []byte(`1`), 0o644))
	missing := filepath.Join(dir, "missing.json")

	tests := map[string]struct {
		command          string
		wantStderrPrefix string
	}{
		"double dash passes dash-prefixed names as files": {
			command:          `jq . -- -x`,
			wantStderrPrefix: "jq: open -x: ",
		},
		"missing file fails before any output": {
			command:          `jq -c . '` + first + `' '` + missing + `'`,
			wantStderrPrefix: "jq: open " + missing + ": ",
		},
		"unreadable file fails as an input error": {
			command:          `jq . '` + dir + `'`,
			wantStderrPrefix: "jq: read " + dir + ": ",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := runJQ(t, tc.command, "ignored")
			require.Equal(t, 2, got.code)
			require.Empty(t, got.stdout)
			require.True(t, strings.HasPrefix(got.stderr, tc.wantStderrPrefix), "stderr = %q", got.stderr)
		})
	}
}

type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

func TestJQBuiltin_WriteError(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"json": `jq .`,
		"raw":  `jq -r .`,
	}
	for name, command := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := errors.New("disk full")

			err := shell.Run(t.Context(), shell.RunOptions{
				Command: command,
				Cwd:     t.TempDir(),
				Stdin:   strings.NewReader(`"x"`),
				Stdout:  errWriter{err: want},
			})

			require.ErrorIs(t, err, want)
		})
	}
}

func TestJQBuiltin_Env(t *testing.T) {
	t.Setenv("ANVIL_JQ_TEST", "from-env")

	got := runJQ(t, `jq -n -r '$ENV.ANVIL_JQ_TEST'`, "")

	require.Equal(t, jqResult{stdout: "from-env\n"}, got)
}

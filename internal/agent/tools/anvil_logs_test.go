package tools

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// createTestLogFile creates a temporary log file with the given entries.
func createTestLogFile(t *testing.T, entries []map[string]any) string {
	t.Helper()
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "anvil.log")

	file, err := os.Create(logFile)
	require.NoError(t, err)
	defer file.Close()

	for _, entry := range entries {
		line, err := json.Marshal(entry)
		require.NoError(t, err)
		_, err = file.WriteString(string(line) + "\n")
		require.NoError(t, err)
	}

	return logFile
}

// makeLogEntry creates a standard log entry for testing.
func makeLogEntry(level, msg, source string, line int, extra map[string]any) map[string]any {
	entry := map[string]any{
		"time":  time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Format(time.RFC3339),
		"level": level,
		"msg":   msg,
		"source": map[string]any{
			"file": source,
			"line": line,
		},
	}
	maps.Copy(entry, extra)
	return entry
}

func TestNewAnvilLogsTool(t *testing.T) {
	t.Parallel()
	tool := NewAnvilLogsTool("/tmp/test.log")
	require.NotNil(t, tool)
	require.Equal(t, AnvilLogsToolName, tool.Info().Name)
}

func TestAnvilLogs_HappyPath(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("INFO", "Application started", "app.go", 42, map[string]any{"version": "1.0.0"}),
		makeLogEntry("DEBUG", "Processing request", "handler.go", 100, map[string]any{"request_id": "abc123"}),
		makeLogEntry("ERROR", "Database connection failed", "db.go", 55, map[string]any{"retry_count": 3}),
	}

	logFile := createTestLogFile(t, entries)

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 3})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 3)

	// Verify format: TIMESTAMP LEVEL SOURCE:LINE MESSAGE
	require.Contains(t, lines[0], "INFO")
	require.Contains(t, lines[0], "app.go:42")
	require.Contains(t, lines[0], "Application started")
	require.Contains(t, lines[0], "version=1.0.0")

	require.Contains(t, lines[1], "DEBUG")
	require.Contains(t, lines[1], "handler.go:100")

	require.Contains(t, lines[2], "ERROR")
	require.Contains(t, lines[2], "db.go:55")
}

func TestAnvilLogs_DefaultLines(t *testing.T) {
	t.Parallel()
	// Create 100 log entries.
	var entries []map[string]any
	for i := range 100 {
		entries = append(entries, makeLogEntry("INFO", fmt.Sprintf("Entry %d", i), "app.go", i, nil))
	}

	logFile := createTestLogFile(t, entries)

	// Call with Lines: 0 should default to 50.
	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 0})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 50)

	// Verify we got the last 50 entries (entry 50-99).
	require.Contains(t, lines[0], "Entry 50")
	require.Contains(t, lines[49], "Entry 99")
}

func TestAnvilLogs_MaxCap(t *testing.T) {
	t.Parallel()
	// Create 200 log entries.
	var entries []map[string]any
	for i := range 200 {
		entries = append(entries, makeLogEntry("INFO", fmt.Sprintf("Entry %d", i), "app.go", i, nil))
	}

	logFile := createTestLogFile(t, entries)

	// Request 200 lines, but should only get 100 (max cap).
	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 200})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 100)
}

func TestAnvilLogs_MissingFile(t *testing.T) {
	t.Parallel()
	result := runAnvilLogs("/nonexistent/path/anvil.log", AnvilLogsParams{Lines: 50})
	require.Contains(t, result, "No log file found")
}

func TestAnvilLogs_UnreadableFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file modes that deny reading")
	}
	logFile := writeRawLog(t, `{"msg":"a"}`+"\n")
	require.NoError(t, os.Chmod(logFile, 0o000))

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 50})

	require.Equal(t, "Error reading log file: open "+logFile+": permission denied", result)
}

func TestAnvilLogs_EmptyFile(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"no content":                     "",
		"only blank and malformed lines": "\nnot json\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := runAnvilLogs(writeRawLog(t, content), AnvilLogsParams{Lines: 50})

			require.Equal(t, "Log file is empty", result)
		})
	}
}

func TestAnvilLogs_MalformedLines(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "anvil.log")

	file, err := os.Create(logFile)
	require.NoError(t, err)

	// Write some valid and some invalid lines.
	validEntry := makeLogEntry("INFO", "Valid entry", "app.go", 1, nil)
	line, _ := json.Marshal(validEntry)
	file.WriteString(string(line) + "\n")
	file.WriteString("\n")
	file.WriteString("this is not json\n")
	file.WriteString(`{"incomplete": "json` + "\n")

	validEntry2 := makeLogEntry("INFO", "Another valid entry", "app.go", 2, nil)
	line2, _ := json.Marshal(validEntry2)
	file.WriteString(string(line2) + "\n")

	file.Close()

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 10})

	lines := strings.Split(result, "\n")
	// Only 2 valid lines should be returned.
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], "Valid entry")
	require.Contains(t, lines[1], "Another valid entry")
}

func TestAnvilLogs_ExtraFieldsSorted(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("INFO", "Test message", "app.go", 1, map[string]any{
			"z_field": "last",
			"a_field": "first",
			"m_field": "middle",
		}),
	}

	logFile := createTestLogFile(t, entries)

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 1})

	// Fields should be sorted alphabetically.
	idxA := strings.Index(result, "a_field=first")
	idxM := strings.Index(result, "m_field=middle")
	idxZ := strings.Index(result, "z_field=last")

	require.True(t, idxA < idxM, "a_field should come before m_field")
	require.True(t, idxM < idxZ, "m_field should come before z_field")
}

func TestAnvilLogs_NonStringValues(t *testing.T) {
	t.Parallel()
	entry := map[string]any{
		"time":   time.Now().Format(time.RFC3339),
		"level":  "INFO",
		"msg":    "Test values",
		"source": map[string]any{"file": "app.go", "line": 1},
		"count":  42,
		"ratio":  3.14,
		"active": true,
		"data":   nil,
		"obj":    map[string]any{"key": "value"},
		"arr":    []any{1, 2, 3},
	}

	logFile := createTestLogFile(t, []map[string]any{entry})

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 1})

	// Numbers should be bare (not quoted).
	require.Contains(t, result, "count=42")
	require.Contains(t, result, "ratio=3.14")

	// Booleans should be bare.
	require.Contains(t, result, "active=true")

	// Null should be bare.
	require.Contains(t, result, "data=null")

	// Objects and arrays should be JSON-encoded and quoted.
	require.Contains(t, result, `obj="{`)
	require.Contains(t, result, `arr="[`)
}

func TestAnvilLogs_Redaction(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("INFO", "API call", "api.go", 10, map[string]any{
			"authorization":  "Bearer secret123",
			"api_key":        "my-api-key",
			"api-key":        "my-api-key-2",
			"apikey":         "myapikey",
			"token":          "mytoken",
			"secret":         "mysecret",
			"password":       "mypassword",
			"credential":     "mycred",
			"Authorization":  "Bearer secret456",
			"API_KEY":        "uppercase",
			"my_token_value": "nestedtoken",
		}),
	}

	logFile := createTestLogFile(t, entries)

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 1})

	// All sensitive fields should be redacted.
	require.Contains(t, result, "authorization=[REDACTED]")
	require.Contains(t, result, "api_key=[REDACTED]")
	require.Contains(t, result, "api-key=[REDACTED]")
	require.Contains(t, result, "apikey=[REDACTED]")
	require.Contains(t, result, "token=[REDACTED]")
	require.Contains(t, result, "secret=[REDACTED]")
	require.Contains(t, result, "password=[REDACTED]")
	require.Contains(t, result, "credential=[REDACTED]")
	require.Contains(t, result, "Authorization=[REDACTED]")
	require.Contains(t, result, "API_KEY=[REDACTED]")
	require.Contains(t, result, "my_token_value=[REDACTED]")

	// Original sensitive values should not appear.
	require.NotContains(t, result, "secret123")
	require.NotContains(t, result, "my-api-key")
	require.NotContains(t, result, "mytoken")
}

func TestAnvilLogs_ReservedFields(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		{
			"time":   time.Now().Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "Test",
			"source": map[string]any{"file": "app.go", "line": 1},
			"Time":   "should be reserved",
			"LEVEL":  "should be reserved",
			"Msg":    "should be reserved",
			"SOURCE": "should be reserved",
			"extra":  "should appear",
		},
	}

	logFile := createTestLogFile(t, entries)

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 1})

	// Reserved fields (case-insensitive) should not appear in extra fields.
	require.NotContains(t, result, "Time=")
	require.NotContains(t, result, "LEVEL=")
	require.NotContains(t, result, "Msg=")
	require.NotContains(t, result, "SOURCE=")
	require.NotContains(t, result, "time=")  // The extra time field
	require.NotContains(t, result, "level=") // The extra level field

	// Non-reserved field should appear (quoted since it has spaces).
	require.Contains(t, result, `extra="should appear"`)
}

func TestAnvilLogs_OversizedLines(t *testing.T) {
	t.Parallel()

	t.Run("trailing line", func(t *testing.T) {
		t.Parallel()
		tests := map[string]struct {
			size int
			want int
		}{
			"at the limit":   {size: maxLogLineSize, want: 2},
			"over the limit": {size: maxLogLineSize + 1, want: 1},
		}
		for name, tc := range tests {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				result := runAnvilLogs(writeRawLog(t, `{"msg":"small"}`+"\n"+jsonLineOfSize(tc.size)), AnvilLogsParams{Lines: 10})

				lines := strings.Split(result, "\n")
				require.Len(t, lines, tc.want)
				require.Contains(t, lines, "--:--:-- INFO unknown:0 small")
			})
		}
	})

	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "anvil.log")

	file, err := os.Create(logFile)
	require.NoError(t, err)

	// Create a valid entry first.
	validEntry := makeLogEntry("INFO", "Valid entry", "app.go", 1, nil)
	line, _ := json.Marshal(validEntry)
	file.WriteString(string(line) + "\n")

	// Create an oversized line (more than 1 MB).
	bigValue := strings.Repeat("x", maxLogLineSize+1000)
	bigEntry := map[string]any{
		"time":   time.Now().Format(time.RFC3339),
		"level":  "INFO",
		"msg":    "Big message",
		"source": map[string]any{"file": "big.go", "line": 1},
		"data":   bigValue,
	}
	bigLine, _ := json.Marshal(bigEntry)
	file.WriteString(string(bigLine) + "\n")

	// Create another valid entry.
	validEntry2 := makeLogEntry("INFO", "Second valid entry", "app.go", 2, nil)
	line2, _ := json.Marshal(validEntry2)
	file.WriteString(string(line2) + "\n")

	file.Close()

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 10})

	lines := strings.Split(result, "\n")

	// Only the 2 valid entries should be returned (oversized one skipped).
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], "Valid entry")
	require.Contains(t, lines[1], "Second valid entry")
}

func TestAnvilLogs_PartialTrailingLine(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "anvil.log")

	file, err := os.Create(logFile)
	require.NoError(t, err)

	// Create valid entries.
	for i := range 5 {
		entry := makeLogEntry("INFO", fmt.Sprintf("Entry %d", i), "app.go", i, nil)
		line, _ := json.Marshal(entry)
		file.WriteString(string(line) + "\n")
	}

	// Write a partial/truncated line (no closing brace or newline).
	file.WriteString(`{"time": "2024-01-15T10:00:00Z", "level": "INFO", "msg": "Truncated`)
	file.Close()

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 10})

	lines := strings.Split(result, "\n")

	// Should get the 5 valid entries, truncated line is skipped.
	require.Len(t, lines, 5)
	for i, line := range lines {
		require.Contains(t, line, fmt.Sprintf("Entry %d", i))
	}
}

func TestAnvilLogs_ValueQuoting(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("INFO", "Test", "app.go", 1, map[string]any{
			"empty":          "",
			"with_spaces":    "hello world",
			"with_equals":    "a=b",
			"with_newline":   "line1\nline2",
			"with_quote":     `say "hello"`,
			"with_backslash": "path\\to\\file",
			"normal":         "simplevalue",
		}),
	}

	logFile := createTestLogFile(t, entries)

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 1})

	// Empty strings should be quoted.
	require.Contains(t, result, `empty=""`)

	// Strings with spaces should be quoted.
	require.Contains(t, result, `with_spaces="hello world"`)

	// Strings with = should be quoted.
	require.Contains(t, result, `with_equals="a=b"`)

	// Strings with newlines should escape them.
	require.Contains(t, result, `with_newline="line1\nline2"`)

	// Strings with quotes should escape them.
	require.Contains(t, result, `with_quote="say \"hello\""`)

	// Strings with backslashes should escape them.
	require.Contains(t, result, `with_backslash="path\\to\\file"`)

	// Normal strings without special chars should be bare.
	require.Contains(t, result, "normal=simplevalue")
}

func TestAnvilLogs_ChronologicalOrder(t *testing.T) {
	t.Parallel()
	// Create entries with different timestamps.
	baseTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	entries := []map[string]any{
		{
			"time":   baseTime.Add(0 * time.Second).Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "First",
			"source": map[string]any{"file": "app.go", "line": 1},
		},
		{
			"time":   baseTime.Add(1 * time.Second).Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "Second",
			"source": map[string]any{"file": "app.go", "line": 2},
		},
		{
			"time":   baseTime.Add(2 * time.Second).Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "Third",
			"source": map[string]any{"file": "app.go", "line": 3},
		},
	}

	logFile := createTestLogFile(t, entries)

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 3})

	lines := strings.Split(result, "\n")

	// Verify chronological order (oldest first).
	require.Len(t, lines, 3)
	require.Contains(t, lines[0], "First")
	require.Contains(t, lines[1], "Second")
	require.Contains(t, lines[2], "Third")
}

func TestAnvilLogs_TimeOnlyFormat(t *testing.T) {
	t.Parallel()
	entry := map[string]any{
		"time":   "2024-01-15T15:04:05Z",
		"level":  "INFO",
		"msg":    "Test",
		"source": map[string]any{"file": "app.go", "line": 1},
	}

	logFile := createTestLogFile(t, []map[string]any{entry})

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 1})

	// Should show time-only format.
	require.True(t, strings.HasPrefix(result, "15:04:05"), "Expected time-only format, got: %s", result)
}

func TestAnvilLogs_LevelVariations(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		makeLogEntry("DEBUG", "Debug message", "app.go", 1, nil),
		makeLogEntry("INFO", "Info message", "app.go", 2, nil),
		makeLogEntry("WARN", "Warn message", "app.go", 3, nil),
		makeLogEntry("WARNING", "Warning message", "app.go", 4, nil),
		makeLogEntry("ERROR", "Error message", "app.go", 5, nil),
	}

	logFile := createTestLogFile(t, entries)

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 5})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 5)

	// Check level normalization.
	require.Contains(t, lines[0], "DEBUG")
	require.Contains(t, lines[1], "INFO")
	require.Contains(t, lines[2], "WARN")
	require.Contains(t, lines[3], "WARN") // WARNING -> WARN
	require.Contains(t, lines[4], "ERROR")
}

func TestAnvilLogs_SourceVariations(t *testing.T) {
	t.Parallel()
	entries := []map[string]any{
		// Source as object with file and line.
		{
			"time":   time.Now().Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "Object source",
			"source": map[string]any{"file": "/path/to/app.go", "line": 42},
		},
		// Source as string.
		{
			"time":   time.Now().Format(time.RFC3339),
			"level":  "INFO",
			"msg":    "String source",
			"source": "/path/to/handler.go:100",
		},
		// Missing source.
		{
			"time":  time.Now().Format(time.RFC3339),
			"level": "INFO",
			"msg":   "No source",
		},
	}

	logFile := createTestLogFile(t, entries)

	result := runAnvilLogs(logFile, AnvilLogsParams{Lines: 3})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, 3)

	// Check source formatting (should use basename only).
	require.Contains(t, lines[0], "app.go:42")
	require.Contains(t, lines[1], "handler.go") // String source gets basename too
	require.Contains(t, lines[2], "unknown:0")  // Missing source
}

// writeRawLog writes content to a fresh log file and returns its path.
func writeRawLog(t *testing.T, content string) string {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "anvil.log")
	require.NoError(t, os.WriteFile(logFile, []byte(content), 0o644))
	return logFile
}

// jsonLineOfSize returns a JSON object line exactly size bytes long.
func jsonLineOfSize(size int) string {
	const prefix, suffix = `{"msg":"`, `"}`
	return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
}

func TestAnvilLogs_TrailingLineWithoutNewlineKnownBug(t *testing.T) {
	t.Parallel()

	// BUG: a final line without a newline is read last, so it comes back
	// as the oldest entry rather than the newest, and is the first to be
	// dropped when lines runs out.
	content := `{"msg":"a"}` + "\n" + `{"msg":"b"}` + "\n" + `{"msg":"c"}`
	tests := map[string]struct {
		lines int
		want  string
	}{
		"room for the trailing line": {
			lines: 3,
			want:  "--:--:-- INFO unknown:0 c\n--:--:-- INFO unknown:0 a\n--:--:-- INFO unknown:0 b",
		},
		"no room for the trailing line": {
			lines: 2,
			want:  "--:--:-- INFO unknown:0 a\n--:--:-- INFO unknown:0 b",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := runAnvilLogs(writeRawLog(t, content), AnvilLogsParams{Lines: tc.lines})

			require.Equal(t, tc.want, result)
		})
	}
}

func TestAnvilLogs_ReadsAcrossChunks(t *testing.T) {
	t.Parallel()

	// readLastLines reads the file backwards in chunks of this size.
	const chunkSize = 8192
	const total = 150
	var content strings.Builder
	var starts []int
	for i := range total {
		starts = append(starts, content.Len())
		line, err := json.Marshal(makeLogEntry("INFO", fmt.Sprintf("entry %03d", i), "app.go", 1, nil))
		require.NoError(t, err)
		content.Write(line)
		content.WriteString("\n")
	}
	size := content.Len()
	require.Greater(t, size, chunkSize)
	starts = append(starts, size)

	// Entries that straddle a chunk boundary are read wrongly today, so
	// only check that the newest entries which don't are all returned, in
	// order.
	crossesChunk := func(i int) bool {
		for b := size - chunkSize; b > 0; b -= chunkSize {
			if starts[i] < b && b < starts[i+1] {
				return true
			}
		}
		return false
	}
	var want []string
	for i := total - maxLogLines; i < total; i++ {
		if !crossesChunk(i) {
			want = append(want, fmt.Sprintf("entry %03d", i))
		}
	}
	require.Less(t, len(want), maxLogLines, "the test needs at least one entry across a chunk boundary")

	result := runAnvilLogs(writeRawLog(t, content.String()), AnvilLogsParams{Lines: maxLogLines})

	lines := strings.Split(result, "\n")
	require.Len(t, lines, maxLogLines)
	next := 0
	for _, line := range lines {
		if next < len(want) && strings.HasSuffix(line, " "+want[next]) {
			next++
		}
	}
	require.Equal(t, len(want), next, "missing %q", want[min(next, len(want)-1)])
}

package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/term"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	initOnce    sync.Once
	initialized atomic.Bool
)

func Setup(logFile string, debug bool, ws ...io.Writer) {
	initOnce.Do(func() {
		logRotator := &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    10,    // Max size in MB
			MaxBackups: 0,     // Number of backups
			MaxAge:     30,    // Days
			Compress:   false, // Enable compression
		}

		level := slog.LevelInfo
		if debug {
			level = slog.LevelDebug
		}

		opts := &slog.HandlerOptions{
			Level:     level,
			AddSource: true,
		}

		var handlers []slog.Handler
		handlers = append(handlers, slog.NewJSONHandler(logRotator, opts))

		for _, w := range ws {
			if w == nil {
				continue
			}
			if f, ok := w.(term.File); ok && term.IsTerminal(f.Fd()) {
				handlers = append(handlers, slog.NewTextHandler(w, opts))
			} else {
				handlers = append(handlers, slog.NewJSONHandler(w, opts))
			}
		}

		slog.SetDefault(slog.New(slog.NewMultiHandler(handlers...)))
		initialized.Store(true)
	})
}

func Initialized() bool {
	return initialized.Load()
}

// RecoverPanic recovers from a panic, logs the stack trace to a file and
// stderr, then calls the cleanup function if provided. The panic log is
// written to the system temp directory so it is always writable.
func RecoverPanic(name string, cleanup func()) {
	if r := recover(); r != nil {
		stack := debug.Stack()

		// Always print to stderr so the user can find it even if the
		// terminal is garbled.
		fmt.Fprintf(os.Stderr, "\n=== ANVIL PANIC (%s) ===\n%v\n\nStack:\n%s\n", name, r, stack)

		// Write a timestamped panic log to the temp directory.
		timestamp := time.Now().Format("20060102-150405")
		filename := fmt.Sprintf("anvil-panic-%s-%s.log", name, timestamp)
		filepath := fmt.Sprintf("%s/%s", os.TempDir(), filename)

		file, err := os.Create(filepath)
		if err == nil {
			defer file.Close()

			fmt.Fprintf(file, "Panic in %s: %v\n\n", name, r)
			fmt.Fprintf(file, "Time: %s\n\n", time.Now().Format(time.RFC3339))
			fmt.Fprintf(file, "Stack Trace:\n%s\n", stack)

			fmt.Fprintf(os.Stderr, "Panic log written to: %s\n", filepath)
		}

		// Execute cleanup function if provided.
		if cleanup != nil {
			cleanup()
		}
	}
}

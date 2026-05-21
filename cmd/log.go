package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	charmlog "github.com/charmbracelet/log"
	"github.com/spf13/cobra"
)

var (
	logFile   string
	logFollow bool
)

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Print the autoshelf log (use -f to tail it)",
	Long: `Stream the autoshelf log file with structured, colorized output.

The daemon writes JSON to its log file under launchd / systemd; this
command parses each record and re-renders it through charmbracelet/log,
which means timestamps, level tags, and key/value attributes get the same
treatment they would in an interactive run. Colors are added when stdout
is a terminal.

Without -f, prints the current contents and exits. With -f, follows the
file the way tail -f does. Ctrl-C exits cleanly.

The default file location matches the StandardErrorPath used in the README's
launchd / systemd templates. Override with --file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := logFile
		if path == "" {
			path = defaultLogPath()
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return streamLog(ctx, path, cmd.OutOrStdout(), logFollow)
	},
}

func init() {
	logCmd.Flags().StringVar(&logFile, "file", "", "log file path (defaults to platform-specific location)")
	logCmd.Flags().BoolVarP(&logFollow, "follow", "f", false, "follow the log as new lines are appended")
	rootCmd.AddCommand(logCmd)
}

// defaultLogPath returns the most likely log file location for the current
// machine. It probes a list of candidate paths in priority order (Homebrew
// service location first, then platform-specific defaults) and returns the
// first one that exists. If none exist on disk yet, the platform default is
// returned so the error from streamLog stays meaningful ("expected at X").
func defaultLogPath() string {
	for _, p := range candidateLogPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return platformDefaultLogPath()
}

// candidateLogPaths returns the ordered list of locations to probe. Order
// matters: Homebrew-managed services write to the brew prefix's var/log/,
// so if both that and the manual-plist Library/Logs path exist, the brew
// one is the one the user just installed and is therefore more current.
func candidateLogPaths() []string {
	var out []string
	if prefix := os.Getenv("HOMEBREW_PREFIX"); prefix != "" {
		out = append(out, filepath.Join(prefix, "var", "log", "autoshelf.log"))
	}
	// Apple Silicon and Intel brew prefixes, in case HOMEBREW_PREFIX is
	// unset (running outside a brew shell).
	out = append(out,
		"/opt/homebrew/var/log/autoshelf.log",
		"/usr/local/var/log/autoshelf.log",
	)
	out = append(out, platformDefaultLogPath())
	return out
}

// platformDefaultLogPath returns the manual-plist / systemd-unit location
// documented in the README, used as the final fallback.
func platformDefaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "autoshelf.log"
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "autoshelf.log")
	default:
		if x := os.Getenv("XDG_STATE_HOME"); x != "" {
			return filepath.Join(x, "autoshelf", "autoshelf.log")
		}
		return filepath.Join(home, ".local", "state", "autoshelf", "autoshelf.log")
	}
}

// streamLog reads JSON log records from path and re-renders them through a
// charm log handler bound to out. When follow is true, the function keeps
// the file open after EOF and polls for new data until ctx is cancelled.
//
// Polling at ~200 ms is deliberately simple - no fsnotify, no native tail,
// just a sleep+read loop. The cost is a tiny ceiling on how quickly new
// lines surface; the win is no platform-specific code and easy debugging.
func streamLog(ctx context.Context, path string, out io.Writer, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("log file does not exist: %s (the daemon may not have started yet, or --file is wrong)", path)
		}
		return err
	}
	defer f.Close()

	handler := newRenderHandler(out)

	const pollInterval = 200 * time.Millisecond
	chunk := make([]byte, 4096)
	var carry []byte // partial line held across reads

	for {
		n, rerr := f.Read(chunk)
		if n > 0 {
			carry = append(carry, chunk[:n]...)
			// Flush any complete lines.
			for {
				idx := bytes.IndexByte(carry, '\n')
				if idx == -1 {
					break
				}
				renderLine(handler, out, carry[:idx])
				carry = carry[idx+1:]
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				return rerr
			}
			if !follow {
				// Flush any final unterminated line.
				if len(carry) > 0 {
					renderLine(handler, out, carry)
				}
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pollInterval):
			}
		}
	}
}

// newRenderHandler builds a charm log handler aimed at the destination
// writer (typically os.Stdout). Colors are added automatically when out is
// a TTY; piped output stays clean for grep / awk.
func newRenderHandler(out io.Writer) slog.Handler {
	return charmlog.NewWithOptions(out, charmlog.Options{
		Level:           charmlog.DebugLevel, // show everything in the file
		ReportTimestamp: true,
		TimeFormat:      "2006-01-02 15:04:05",
	})
}

// renderLine decodes one JSON log line and dispatches it to the charm log
// handler with the original timestamp / level / attrs preserved. Non-JSON
// lines are written through verbatim so we don't drop occasional plain
// stderr output that the daemon emitted before the structured logger
// initialized (or that came from a panic, etc.).
func renderLine(h slog.Handler, fallback io.Writer, line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	if line[0] != '{' {
		fmt.Fprintln(fallback, string(line))
		return
	}

	var rec map[string]any
	if err := json.Unmarshal(line, &rec); err != nil {
		fmt.Fprintln(fallback, string(line))
		return
	}

	// Pull the well-known fields aside; everything else becomes attrs.
	t := parseRecTime(rec)
	lvl := parseRecLevel(rec)
	msg, _ := rec["msg"].(string)

	delete(rec, "time")
	delete(rec, "level")
	delete(rec, "msg")

	slogRec := slog.NewRecord(t, lvl, msg, 0)
	for k, v := range rec {
		slogRec.AddAttrs(slog.Any(k, v))
	}
	if err := h.Handle(context.Background(), slogRec); err != nil {
		fmt.Fprintln(fallback, string(line))
	}
}

func parseRecTime(rec map[string]any) time.Time {
	ts, ok := rec["time"].(string)
	if !ok {
		return time.Now()
	}
	// Try the formats we're likely to see, most-specific first.
	for _, f := range []string{
		"2006-01-02T15:04:05.000Z07:00",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(f, ts); err == nil {
			return t
		}
	}
	return time.Now()
}

func parseRecLevel(rec map[string]any) slog.Level {
	l, _ := rec["level"].(string)
	switch strings.ToLower(l) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

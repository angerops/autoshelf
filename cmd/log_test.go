package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// defaultLogPath: on darwin the home Library Logs path is expected; on Linux
// we honor XDG_STATE_HOME if set, else ~/.local/state/...

func TestPlatformDefaultLogPathDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only path test")
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Logs", "autoshelf.log")
	if got := platformDefaultLogPath(); got != want {
		t.Errorf("platformDefaultLogPath darwin: got %q want %q", got, want)
	}
}

func TestPlatformDefaultLogPathLinuxXDGOverride(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin path test")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)
	want := filepath.Join(tmp, "autoshelf", "autoshelf.log")
	if got := platformDefaultLogPath(); got != want {
		t.Errorf("platformDefaultLogPath XDG override: got %q want %q", got, want)
	}
}

func TestPlatformDefaultLogPathLinuxDefault(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin path test")
	}
	t.Setenv("XDG_STATE_HOME", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "state", "autoshelf", "autoshelf.log")
	if got := platformDefaultLogPath(); got != want {
		t.Errorf("platformDefaultLogPath default: got %q want %q", got, want)
	}
}

// HOMEBREW_PREFIX must appear first in the candidate list when set, so a
// brew-managed install wins over the manual-plist default.
func TestCandidateLogPathsHomebrewPrefixFirst(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "/custom/brew")
	got := candidateLogPaths()
	if len(got) == 0 || got[0] != "/custom/brew/var/log/autoshelf.log" {
		t.Errorf("HOMEBREW_PREFIX path should be first: got %v", got)
	}
}

// Even without HOMEBREW_PREFIX set, the two standard brew prefixes are
// probed so a user outside a brew shell still finds the right file.
func TestCandidateLogPathsAlwaysIncludeStandardBrewPrefixes(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "")
	got := candidateLogPaths()
	hasOpt, hasUsr := false, false
	for _, p := range got {
		if p == "/opt/homebrew/var/log/autoshelf.log" {
			hasOpt = true
		}
		if p == "/usr/local/var/log/autoshelf.log" {
			hasUsr = true
		}
	}
	if !hasOpt || !hasUsr {
		t.Errorf("standard brew prefix paths missing from candidates: %v", got)
	}
}

// defaultLogPath should pick the first candidate that actually exists on
// disk, falling back to the platform default if none do.
func TestDefaultLogPathPicksExistingFile(t *testing.T) {
	// Stage a fake brew log so it precedes any real platform default.
	dir := t.TempDir()
	t.Setenv("HOMEBREW_PREFIX", dir)
	logPath := filepath.Join(dir, "var", "log", "autoshelf.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := defaultLogPath(); got != logPath {
		t.Errorf("defaultLogPath should pick the existing brew path: got %q want %q", got, logPath)
	}
}

// When no candidate file exists, defaultLogPath returns the platform default
// so the error message from streamLog points at the most likely intended
// location.
func TestDefaultLogPathFallsBackToPlatformDefault(t *testing.T) {
	t.Setenv("HOMEBREW_PREFIX", "/nonexistent/brew/prefix")
	if got, want := defaultLogPath(), platformDefaultLogPath(); got != want {
		t.Errorf("fallback: got %q want %q", got, want)
	}
}

// A JSON log line in the file must be parsed and rendered as structured
// text containing the message and the attribute key=value pairs.
func TestStreamLogRendersJSONRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	line := `{"time":"2026-05-20T10:42:15.123-04:00","level":"info","msg":"moved","rule":"PDFs","src":"/in/foo.pdf"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := streamLog(context.Background(), path, &buf, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// Message and attributes must appear; level word should appear.
	if !strings.Contains(out, "moved") {
		t.Errorf("rendered output missing msg: %q", out)
	}
	if !strings.Contains(out, "rule") || !strings.Contains(out, "PDFs") {
		t.Errorf("rendered output missing rule attr: %q", out)
	}
	if !strings.Contains(out, "/in/foo.pdf") {
		t.Errorf("rendered output missing src attr: %q", out)
	}
	// Raw JSON should NOT pass through as-is when parsing succeeds.
	if strings.Contains(out, `"msg":"moved"`) {
		t.Errorf("output should not contain raw JSON: %q", out)
	}
}

// Lines that aren't JSON (panic stack traces, legacy plain text) must be
// streamed through unchanged so the user still sees them.
func TestStreamLogPassesThroughNonJSONLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "panic: something exploded\ngoroutine 1 [running]:\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := streamLog(context.Background(), path, &buf, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "panic: something exploded") {
		t.Errorf("non-JSON line lost: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "goroutine 1") {
		t.Errorf("second non-JSON line lost: %q", buf.String())
	}
}

func TestStreamLogMissingFileReturnsHelpfulError(t *testing.T) {
	err := streamLog(context.Background(), "/no/such/path/autoshelf.log", &bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("expected error for missing log file")
	}
	if !strings.Contains(err.Error(), "log file does not exist") {
		t.Errorf("error should explain the file is missing, got %v", err)
	}
}

// Malformed JSON (truncated, etc.) must not crash - it falls through to the
// plain-text writer.
func TestStreamLogHandlesMalformedJSONLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	line := `{"time":"2026-05-20T10:42:15Z","level":"info","msg":"trunca` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := streamLog(context.Background(), path, &buf, false); err != nil {
		t.Fatal(err)
	}
	// We don't care about the exact form, but it shouldn't be empty: the
	// raw line should pass through.
	if !strings.Contains(buf.String(), "trunca") {
		t.Errorf("malformed JSON should pass through verbatim, got %q", buf.String())
	}
}

// safeBuffer is a bytes.Buffer protected by a mutex so the streamLog
// goroutine and the test goroutine can interact safely.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// In follow mode, new appends to the file must reach the writer.
func TestStreamLogFollowsAppendsUntilCancel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	initial := `{"time":"2026-05-20T10:42:15Z","level":"info","msg":"started"}` + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf safeBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- streamLog(ctx, path, &buf, true)
	}()

	// Wait for the initial record to render.
	deadline := time.After(time.Second)
	for !strings.Contains(buf.String(), "started") {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("initial record never rendered; buf=%q", buf.String())
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Append more, wait for the 200ms poll cycle.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	follower := `{"time":"2026-05-20T10:42:20Z","level":"info","msg":"appended","action":"move"}` + "\n"
	if _, err := f.WriteString(follower); err != nil {
		t.Fatal(err)
	}
	f.Close()

	deadline = time.After(2 * time.Second)
	for !strings.Contains(buf.String(), "appended") {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("appended record never rendered; buf=%q", buf.String())
		case <-time.After(50 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("streamLog returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("streamLog did not return after context cancel")
	}
}

// parseRecLevel covers the slog level mapping branches.
func TestParseRecLevel(t *testing.T) {
	cases := map[string]string{
		"debug":   "DEBUG",
		"info":    "INFO",
		"warn":    "WARN",
		"warning": "WARN",
		"error":   "ERROR",
		"err":     "ERROR",
		"":        "INFO", // default
		"weird":   "INFO", // default
	}
	for in, want := range cases {
		got := parseRecLevel(map[string]any{"level": in}).String()
		if got != want {
			t.Errorf("parseRecLevel(%q): got %q want %q", in, got, want)
		}
	}
}

// parseRecTime falls back to "now" on missing/unparseable input, otherwise
// returns the parsed time.
func TestParseRecTime(t *testing.T) {
	want, _ := time.Parse(time.RFC3339, "2026-05-20T10:42:15-04:00")
	got := parseRecTime(map[string]any{"time": "2026-05-20T10:42:15-04:00"})
	if !got.Equal(want) {
		t.Errorf("parseRecTime parsed: got %v want %v", got, want)
	}
	// Missing time falls back to ~now.
	now := time.Now()
	got = parseRecTime(map[string]any{})
	if delta := got.Sub(now); delta < -time.Second || delta > time.Second {
		t.Errorf("parseRecTime fallback should be ~now, drift %v", delta)
	}
}

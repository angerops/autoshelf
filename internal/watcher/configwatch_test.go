package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// changeRecorder is a goroutine-safe counter for the WatchConfigFile callback.
// We need a separate channel-based notification so tests can block on the
// next fire without polling.
type changeRecorder struct {
	count  atomic.Int32
	notify chan struct{}

	mu      sync.Mutex
	history []time.Time
}

func newChangeRecorder() *changeRecorder {
	return &changeRecorder{notify: make(chan struct{}, 64)}
}

func (r *changeRecorder) fire() {
	r.count.Add(1)
	r.mu.Lock()
	r.history = append(r.history, time.Now())
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *changeRecorder) waitFor(want int32, timeout time.Duration) int32 {
	deadline := time.After(timeout)
	for {
		if got := r.count.Load(); got >= want {
			return got
		}
		select {
		case <-r.notify:
		case <-deadline:
			return r.count.Load()
		}
	}
}

// startConfigWatch boots WatchConfigFile in a goroutine with the supplied
// debounce and returns the recorder + a cancel/wait pair. Every test cleans
// up via t.Cleanup so a missing cancel doesn't leak watchers across tests.
func startConfigWatch(t *testing.T, path string, debounce time.Duration) (*changeRecorder, context.CancelFunc) {
	t.Helper()
	rec := newChangeRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WatchConfigFile(ctx, path, debounce, rec.fire, testLogger())
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("WatchConfigFile did not exit after context cancel")
		}
	})
	// Give fsnotify a moment to register the directory watch before the
	// test starts mutating the file; otherwise the first write race-loses.
	time.Sleep(40 * time.Millisecond)
	return rec, cancel
}

// Empty path is a programmer error - reject it loudly.
func TestWatchConfigFileRejectsEmptyPath(t *testing.T) {
	err := WatchConfigFile(context.Background(), "", 0, func() {}, testLogger())
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

// nil onChange is a programmer error too.
func TestWatchConfigFileRejectsNilOnChange(t *testing.T) {
	err := WatchConfigFile(context.Background(), "/tmp/x", 0, nil, testLogger())
	if err == nil {
		t.Fatal("expected error for nil onChange")
	}
}

// The basic contract: in-place write to the file fires onChange.
func TestWatchConfigFileFiresOnWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoshelf.yaml")
	if err := os.WriteFile(path, []byte("initial: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, _ := startConfigWatch(t, path, 60*time.Millisecond)

	if err := os.WriteFile(path, []byte("initial: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := rec.waitFor(1, time.Second); got != 1 {
		t.Errorf("expected exactly one onChange after write, got %d", got)
	}
}

// Vim and VS Code save by writing a temp file and renaming over the
// target. The watch must catch the rename.
func TestWatchConfigFileFiresOnAtomicRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoshelf.yaml")
	if err := os.WriteFile(path, []byte("initial: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, _ := startConfigWatch(t, path, 60*time.Millisecond)

	tmp := filepath.Join(dir, ".autoshelf.yaml.swp")
	if err := os.WriteFile(tmp, []byte("initial: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}

	if got := rec.waitFor(1, time.Second); got != 1 {
		t.Errorf("expected exactly one onChange after atomic rename, got %d", got)
	}
}

// File created later (didn't exist when the watch started) should still
// trigger - matches the case where the user removes the file briefly and
// the editor recreates it.
func TestWatchConfigFileFiresOnCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoshelf.yaml")
	// Note: file does NOT exist yet.

	rec, _ := startConfigWatch(t, path, 60*time.Millisecond)

	if err := os.WriteFile(path, []byte("initial: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := rec.waitFor(1, time.Second); got != 1 {
		t.Errorf("expected one onChange after create, got %d", got)
	}
}

// Several writes within the debounce window must coalesce into a single
// callback. Same contract as the file-event debounce.
func TestWatchConfigFileCoalescesRapidWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoshelf.yaml")
	if err := os.WriteFile(path, []byte("v: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, _ := startConfigWatch(t, path, 150*time.Millisecond)

	for i := 0; i < 5; i++ {
		if err := os.WriteFile(path, []byte("v: "+string(rune('a'+i))+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Millisecond)
	}

	if got := rec.waitFor(1, 800*time.Millisecond); got != 1 {
		t.Errorf("expected coalesced single onChange, got %d", got)
	}
	// Wait past the debounce + a margin to be sure no extra fire arrives.
	time.Sleep(300 * time.Millisecond)
	if final := rec.count.Load(); final != 1 {
		t.Errorf("late callback observed; total fires=%d", final)
	}
}

// A write to a different file in the same parent directory must NOT fire
// the callback. The watch is per-file, not per-directory, even though
// implementation-wise we watch the parent.
func TestWatchConfigFileIgnoresSiblingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoshelf.yaml")
	if err := os.WriteFile(path, []byte("v: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(dir, "other.yaml")

	rec, _ := startConfigWatch(t, path, 60*time.Millisecond)

	if err := os.WriteFile(sibling, []byte("noise\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even Chmod the target - should still not fire.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)
	if got := rec.count.Load(); got != 0 {
		t.Errorf("sibling/chmod activity should not fire onChange, got %d", got)
	}
}

// configEventMatches is pure logic; unit-test the branches so we don't
// rely on fsnotify integration to cover them all.
func TestConfigEventMatches(t *testing.T) {
	cases := []struct {
		name   string
		target string
		evName string
		op     fsnotify.Op
		want   bool
	}{
		{"match write", "cfg.yaml", "/dir/cfg.yaml", fsnotify.Write, true},
		{"match create", "cfg.yaml", "/dir/cfg.yaml", fsnotify.Create, true},
		{"match rename", "cfg.yaml", "/dir/cfg.yaml", fsnotify.Rename, true},
		{"match combined", "cfg.yaml", "/dir/cfg.yaml", fsnotify.Create | fsnotify.Write, true},
		{"ignore chmod alone", "cfg.yaml", "/dir/cfg.yaml", fsnotify.Chmod, false},
		{"ignore remove alone", "cfg.yaml", "/dir/cfg.yaml", fsnotify.Remove, false},
		{"wrong basename", "cfg.yaml", "/dir/other.yaml", fsnotify.Write, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := fsnotify.Event{Name: tc.evName, Op: tc.op}
			got := configEventMatches(ev, tc.target)
			if got != tc.want {
				t.Errorf("configEventMatches(%s, op=%v): got %v want %v",
					tc.evName, tc.op, got, tc.want)
			}
		})
	}
}

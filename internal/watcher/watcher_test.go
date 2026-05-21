package watcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/angerops/autoshelf/internal/config"
	"github.com/angerops/autoshelf/internal/rules"
)

// fakeEngine is a counting Engine for tests. It records every HandleEntry
// call, lets a test inject a deferral / error response, and notifies via a
// channel so tests can wait without polling.
type fakeEngine struct {
	mu     sync.Mutex
	calls  []string // paths passed to HandleEntry, in order
	scans  atomic.Int32
	notify chan struct{}

	// scanDeferred is what ScanOnce should return as its deferred slice.
	scanDeferred []rules.Deferred

	// deferUntil, if set, is returned from HandleEntry for the given path the
	// first time it's called, after which the path moves to "apply normally".
	// This lets us test the re-queue loop: first call returns a retryAt,
	// subsequent calls return applied=true.
	deferUntil    map[string]time.Time
	deferConsumed map[string]bool
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		notify:        make(chan struct{}, 64),
		deferUntil:    map[string]time.Time{},
		deferConsumed: map[string]bool{},
	}
}

func (f *fakeEngine) HandleEntry(_ *config.Watch, path string) (bool, time.Time, error) {
	f.mu.Lock()
	f.calls = append(f.calls, path)
	if at, ok := f.deferUntil[path]; ok && !f.deferConsumed[path] {
		f.deferConsumed[path] = true
		f.mu.Unlock()
		select {
		case f.notify <- struct{}{}:
		default:
		}
		return false, at, nil
	}
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return true, time.Time{}, nil
}

func (f *fakeEngine) ScanOnce() (int, int, []rules.Deferred, error) {
	f.scans.Add(1)
	f.mu.Lock()
	d := make([]rules.Deferred, len(f.scanDeferred))
	copy(d, f.scanDeferred)
	f.mu.Unlock()
	return 0, 0, d, nil
}

// deferOnce arranges for HandleEntry to return (false, at, nil) on the first
// call for path, then behave normally on the second.
func (f *fakeEngine) deferOnce(path string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deferUntil[path] = at
}

// callsSnapshot returns a copy of the recorded calls so callers can inspect
// them without holding the mutex.
func (f *fakeEngine) callsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// waitFor blocks until the fake engine has accumulated `want` calls or the
// timeout expires. Returns the actual call count seen.
func (f *fakeEngine) waitFor(want int, timeout time.Duration) int {
	deadline := time.After(timeout)
	for {
		f.mu.Lock()
		got := len(f.calls)
		f.mu.Unlock()
		if got >= want {
			return got
		}
		select {
		case <-f.notify:
		case <-deadline:
			return got
		}
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// -----------------------------------------------------------------------------
// collectDirs - pure function tests
// -----------------------------------------------------------------------------

func TestCollectDirsNonRecursive(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirs, err := collectDirs(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != root {
		t.Errorf("non-recursive should return only root, got %v", dirs)
	}
}

func TestCollectDirsRecursive(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	deep := filepath.Join(sub, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file alongside the dirs should not appear in the result.
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs, err := collectDirs(root, true)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(dirs)
	want := []string{root, sub, deep}
	sort.Strings(want)
	if len(dirs) != len(want) {
		t.Fatalf("dir count: got %d (%v) want %d (%v)", len(dirs), dirs, len(want), want)
	}
	for i := range want {
		if dirs[i] != want[i] {
			t.Errorf("dir[%d]: got %q want %q", i, dirs[i], want[i])
		}
	}
}

func TestCollectDirsErrorOnMissing(t *testing.T) {
	_, err := collectDirs(filepath.Join(t.TempDir(), "nope"), false)
	if err == nil {
		t.Error("expected error for missing path")
	}
}

func TestCollectDirsErrorOnFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := collectDirs(file, false)
	if err == nil {
		t.Error("expected error for non-directory path")
	}
}

// -----------------------------------------------------------------------------
// schedule / debounce tests - exercised directly, no fsnotify needed
// -----------------------------------------------------------------------------

// newTestWatcher builds a Watcher with a short debounce window and a fake
// engine for synchronous-feeling tests.
func newTestWatcher(t *testing.T, debounce time.Duration) (*Watcher, *fakeEngine, *config.Watch) {
	t.Helper()
	dir := t.TempDir()
	wc := &config.Watch{Path: dir}
	cfg := &config.Config{Watches: []config.Watch{*wc}}
	fake := newFakeEngine()
	w := New(cfg, fake, testLogger())
	w.debounce = debounce
	return w, fake, &cfg.Watches[0]
}

func TestScheduleFiresAfterDebounce(t *testing.T) {
	w, fake, wc := newTestWatcher(t, 20*time.Millisecond)
	target := filepath.Join(wc.Path, "x")

	w.schedule(context.Background(), wc, target)

	got := fake.waitFor(1, 500*time.Millisecond)
	if got != 1 {
		t.Errorf("expected 1 HandleEntry call after debounce, got %d", got)
	}
	if calls := fake.callsSnapshot(); len(calls) != 1 || calls[0] != target {
		t.Errorf("call path: got %v want [%s]", calls, target)
	}
}

// Rapid-fire events for the same path within the debounce window must coalesce
// to one HandleEntry call. This is the core debounce contract.
func TestScheduleCoalescesDuplicateEvents(t *testing.T) {
	w, fake, wc := newTestWatcher(t, 50*time.Millisecond)
	target := filepath.Join(wc.Path, "x")

	// Five rapid events; each Reset()'s the timer, so the engine should only
	// hear once, well after the last call.
	for i := 0; i < 5; i++ {
		w.schedule(context.Background(), wc, target)
		time.Sleep(5 * time.Millisecond)
	}

	got := fake.waitFor(1, 500*time.Millisecond)
	if got != 1 {
		t.Errorf("debounce should coalesce 5 events into 1, got %d", got)
	}
	// Give any spurious extra timers a chance to fire before asserting final count.
	time.Sleep(100 * time.Millisecond)
	if final := len(fake.callsSnapshot()); final != 1 {
		t.Errorf("late-firing extra calls observed: total=%d", final)
	}
}

// Distinct paths must each get their own debounce window and HandleEntry call.
func TestScheduleDistinctPathsBothFire(t *testing.T) {
	w, fake, wc := newTestWatcher(t, 20*time.Millisecond)
	a := filepath.Join(wc.Path, "a")
	b := filepath.Join(wc.Path, "b")

	w.schedule(context.Background(), wc, a)
	w.schedule(context.Background(), wc, b)

	got := fake.waitFor(2, 500*time.Millisecond)
	if got != 2 {
		t.Errorf("expected 2 HandleEntry calls (one per path), got %d", got)
	}
}

// After the timer fires, the pending-map entry must be cleaned up so memory
// doesn't grow without bound when the watcher runs for a long time.
func TestSchedulePendingMapCleanup(t *testing.T) {
	w, fake, wc := newTestWatcher(t, 20*time.Millisecond)
	target := filepath.Join(wc.Path, "x")

	w.schedule(context.Background(), wc, target)
	fake.waitFor(1, 500*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // let the delete in the callback run

	w.mu.Lock()
	size := len(w.pending)
	w.mu.Unlock()
	if size != 0 {
		t.Errorf("pending map should be empty after debounce fires, has %d entries", size)
	}
}

// If the context is already cancelled when the debounce expires, HandleEntry
// must not run. Protects against work happening during shutdown.
func TestScheduleRespectsContextCancel(t *testing.T) {
	w, fake, wc := newTestWatcher(t, 30*time.Millisecond)
	target := filepath.Join(wc.Path, "x")

	ctx, cancel := context.WithCancel(context.Background())
	w.schedule(ctx, wc, target)
	cancel() // cancel before debounce elapses

	time.Sleep(200 * time.Millisecond)
	if calls := fake.callsSnapshot(); len(calls) != 0 {
		t.Errorf("HandleEntry should not run after ctx cancel, got %v", calls)
	}
}

// -----------------------------------------------------------------------------
// End-to-end Run() tests over a real fsnotify
// -----------------------------------------------------------------------------

// startWatcher boots Run in a goroutine and returns a cancel + a wait-for-exit
// helper. Used by every end-to-end test.
func startWatcher(t *testing.T, w *Watcher) (cancel context.CancelFunc, waitExit func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx) // ctx.Canceled on shutdown - expected
	}()
	return cancel, func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("watcher did not shut down within 2s of cancel")
		}
	}
}

func TestRunInitialScanRunsOnStart(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Watches: []config.Watch{{Path: dir}}}
	fake := newFakeEngine()
	w := New(cfg, fake, testLogger())
	w.debounce = 20 * time.Millisecond

	cancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer cancel()

	// The initial scan runs in a goroutine - wait briefly for it to land.
	deadline := time.After(time.Second)
	for fake.scans.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("initial ScanOnce never ran")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestRunCreatedFileTriggersHandleEntry(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Watches: []config.Watch{{Path: dir}}}
	fake := newFakeEngine()
	w := New(cfg, fake, testLogger())
	w.debounce = 50 * time.Millisecond

	cancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer cancel()

	// Give the watcher a moment to register with fsnotify before we create.
	time.Sleep(100 * time.Millisecond)

	target := filepath.Join(dir, "new.pdf")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Initial scan also calls HandleEntry on nothing (empty dir at start), so
	// we look specifically for our target path.
	deadline := time.After(2 * time.Second)
	for {
		for _, c := range fake.callsSnapshot() {
			if c == target {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("HandleEntry never called for %s; saw %v", target, fake.callsSnapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Remove and Chmod events must be filtered out. We trigger a Remove and verify
// the engine never sees the path.
func TestRunRemoveEventIgnored(t *testing.T) {
	dir := t.TempDir()
	// Pre-create the file so the initial-scan won't fire on it (initial scan
	// does fire on existing files; we want a clean baseline of "no calls for
	// this path since startup").
	target := filepath.Join(dir, "doomed")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Watches: []config.Watch{{Path: dir}}}
	fake := newFakeEngine()
	w := New(cfg, fake, testLogger())
	w.debounce = 30 * time.Millisecond

	cancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer cancel()

	// Let the initial scan complete and observe its call so we can ignore it.
	time.Sleep(200 * time.Millisecond)
	baseline := len(fake.callsSnapshot())

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	// Wait long enough for any (incorrect) debounce to fire.
	time.Sleep(300 * time.Millisecond)

	for _, c := range fake.callsSnapshot()[baseline:] {
		if c == target {
			t.Errorf("Remove event should not trigger HandleEntry, but got call for %s", target)
		}
	}
}

func TestRunReturnsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Watches: []config.Watch{{Path: dir}}}
	fake := newFakeEngine()
	w := New(cfg, fake, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx)
	}()

	// Give Run time to settle into its select loop, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run should return context.Canceled on cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after cancel")
	}
}

// Recursive watch: dropping a file into a new subdirectory created after Run
// started must still be observed (the watcher must add the new subdir to
// fsnotify).
func TestRunRecursiveAutoAddsNewSubdir(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Watches: []config.Watch{{Path: root, Recursive: true}}}
	fake := newFakeEngine()
	w := New(cfg, fake, testLogger())
	w.debounce = 50 * time.Millisecond

	cancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer cancel()
	time.Sleep(100 * time.Millisecond)

	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// The subdir creation itself flows through schedule (and our fake engine
	// happily "handles" it). Now drop a file inside the new subdir.
	time.Sleep(200 * time.Millisecond) // give the watcher time to fsw.Add(sub)
	leaf := filepath.Join(sub, "leaf.txt")
	if err := os.WriteFile(leaf, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		for _, c := range fake.callsSnapshot() {
			if c == leaf {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("HandleEntry never called for %s inside new subdir; saw %v", leaf, fake.callsSnapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// -----------------------------------------------------------------------------
// Deferral / min_age re-queue tests
// -----------------------------------------------------------------------------

// When HandleEntry returns a retryAt, the watcher must re-call HandleEntry at
// (or after) retryAt. This is what makes the min_age "wait 5 minutes for the
// Finder rename to finish" behavior work end-to-end.
func TestScheduleReQueuesOnRetryAt(t *testing.T) {
	w, fake, wc := newTestWatcher(t, 20*time.Millisecond)
	target := filepath.Join(wc.Path, "x")

	// First HandleEntry call defers ~80ms; second call should apply normally.
	fake.deferOnce(target, time.Now().Add(80*time.Millisecond))

	w.schedule(context.Background(), wc, target)

	// We expect exactly 2 calls: the initial debounce-fired one (which
	// defers) and the re-queued one (which applies).
	got := fake.waitFor(2, 1*time.Second)
	if got < 2 {
		t.Errorf("expected 2 HandleEntry calls (defer then apply), got %d", got)
	}
	// Both calls should be for the same path.
	for i, p := range fake.callsSnapshot() {
		if p != target {
			t.Errorf("call %d for wrong path: got %q want %q", i, p, target)
		}
	}
}

// Deferred entries reported by the initial scan must be re-queued at their
// RetryAt so they're eventually processed even without a new fsnotify event.
func TestInitialScanDeferredAreReQueued(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Watches: []config.Watch{{Path: dir}}}
	fake := newFakeEngine()

	// Plant a deferred entry inside the watched dir before starting Run.
	target := filepath.Join(dir, "fresh")
	fake.mu.Lock()
	fake.scanDeferred = []rules.Deferred{{Path: target, RetryAt: time.Now().Add(80 * time.Millisecond)}}
	fake.mu.Unlock()

	w := New(cfg, fake, testLogger())
	w.debounce = 20 * time.Millisecond

	cancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer cancel()

	// At 80ms post-start, the re-queue timer should fire and HandleEntry
	// should be called for the deferred target.
	deadline := time.After(1 * time.Second)
	for {
		for _, c := range fake.callsSnapshot() {
			if c == target {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("deferred entry %s was not re-queued by initial scan; calls=%v", target, fake.callsSnapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// watchFor must correctly resolve a path back to its owning Watch entry,
// preferring the longest matching root when watches nest.
func TestWatchForResolvesLongestRoot(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "nested")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Watches: []config.Watch{
			{Path: outer},
			{Path: inner},
		},
	}
	w := New(cfg, newFakeEngine(), testLogger())

	got := w.watchFor(filepath.Join(inner, "leaf.txt"))
	if got == nil || got.Path != inner {
		t.Errorf("watchFor longest-root: got %v want %s", got, inner)
	}
	got = w.watchFor(filepath.Join(outer, "top.txt"))
	if got == nil || got.Path != outer {
		t.Errorf("watchFor outer-root: got %v want %s", got, outer)
	}
	got = w.watchFor("/elsewhere/file.txt")
	if got != nil {
		t.Errorf("watchFor unrelated: got %v want nil", got)
	}
}

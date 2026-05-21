package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/angerops/autoshelf/internal/config"
	"github.com/angerops/autoshelf/internal/rules"
)

// trackedEngine wraps a fakeEngine and remembers the cfg pointer it was
// constructed from. Used to verify that engineFactory ran (engine was
// rebuilt) and which generation of cfg the active engine belongs to.
type trackedEngine struct {
	*fakeEngine
	cfg *config.Config
}

func newTrackedEngine(cfg *config.Config) *trackedEngine {
	return &trackedEngine{fakeEngine: newFakeEngine(), cfg: cfg}
}

// nilArg rejects nil arguments - keeps ApplyConfig contract honest.
func TestApplyConfigRejectsNilArgs(t *testing.T) {
	w, _, _ := newTestWatcher(t, 10*time.Millisecond)

	if err := w.ApplyConfig(context.Background(), nil, func(*config.Config) Engine { return newFakeEngine() }); err == nil {
		t.Error("expected error for nil cfg")
	}
	cfg := &config.Config{Watches: []config.Watch{{Path: t.TempDir()}}}
	if err := w.ApplyConfig(context.Background(), cfg, nil); err == nil {
		t.Error("expected error for nil engineFactory")
	}
}

// ApplyConfig swaps the engine. After the swap, HandleEntry calls land on
// the new engine, not the old one.
func TestApplyConfigSwapsEngine(t *testing.T) {
	w, oldFake, wc := newTestWatcher(t, 10*time.Millisecond)

	newCfg := &config.Config{Watches: []config.Watch{{Path: wc.Path}}}
	newFake := newFakeEngine()
	if err := w.ApplyConfig(context.Background(), newCfg, func(*config.Config) Engine { return newFake }); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Fire one event via the public schedule path; the NEW engine should see it.
	target := filepath.Join(wc.Path, "after-reload")
	w.schedule(context.Background(), wc, target)

	if got := newFake.waitFor(1, 500*time.Millisecond); got != 1 {
		t.Errorf("new engine should have received the call, got %d", got)
	}
	if got := len(oldFake.callsSnapshot()); got != 0 {
		t.Errorf("old engine should not have been called post-reload, got %d", got)
	}
}

// ApplyConfig errors out cleanly if a watch path doesn't exist on disk.
// State should be unchanged: the live engine is still the old one.
func TestApplyConfigErrorDoesNotMutateState(t *testing.T) {
	w, oldFake, wc := newTestWatcher(t, 10*time.Millisecond)

	badCfg := &config.Config{Watches: []config.Watch{{Path: "/definitely/does/not/exist/anywhere"}}}
	factoryCalled := atomic.Bool{}
	err := w.ApplyConfig(context.Background(), badCfg, func(*config.Config) Engine {
		factoryCalled.Store(true)
		return newFakeEngine()
	})
	if err == nil {
		t.Fatal("expected error for missing watch path")
	}
	if factoryCalled.Load() {
		t.Error("engineFactory should not be called if dir resolution fails")
	}

	// Verify the original engine is still wired up.
	target := filepath.Join(wc.Path, "x")
	w.schedule(context.Background(), wc, target)
	if got := oldFake.waitFor(1, 300*time.Millisecond); got != 1 {
		t.Errorf("original engine should still be active after failed reload, got %d calls", got)
	}
}

// After ApplyConfig the watcher kicks off a fresh ScanOnce so new rules
// reach existing files.
func TestApplyConfigTriggersScanOnce(t *testing.T) {
	w, _, wc := newTestWatcher(t, 10*time.Millisecond)

	newCfg := &config.Config{Watches: []config.Watch{{Path: wc.Path}}}
	newFake := newFakeEngine()
	if err := w.ApplyConfig(context.Background(), newCfg, func(*config.Config) Engine { return newFake }); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	deadline := time.After(time.Second)
	for newFake.scans.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("post-reload ScanOnce never ran")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// runHandle must re-resolve the watch via watchFor on the CURRENT cfg.
// A timer scheduled before ApplyConfig with a hint pointing at the old
// Watch struct should still get evaluated against the new cfg.
func TestRunHandleReresolvesWatchAfterReload(t *testing.T) {
	w, _, wc := newTestWatcher(t, 60*time.Millisecond)

	// Capture the OLD watch struct pointer as the schedule hint.
	oldHint := wc
	target := filepath.Join(wc.Path, "x")
	w.schedule(context.Background(), oldHint, target)

	// Before the debounce expires, swap in a new cfg whose Watch entry
	// covers the same path but is a different struct.
	newCfg := &config.Config{Watches: []config.Watch{{Path: wc.Path}}}
	newFake := newFakeEngine()
	if err := w.ApplyConfig(context.Background(), newCfg, func(*config.Config) Engine { return newFake }); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	if got := newFake.waitFor(1, 500*time.Millisecond); got != 1 {
		t.Errorf("post-reload engine should receive the scheduled call, got %d", got)
	}
}

// If the reloaded cfg no longer covers a path that has a pending timer,
// runHandle must drop the call quietly instead of crashing or calling
// the engine with a stale watch.
func TestRunHandleDropsPathNoLongerCovered(t *testing.T) {
	w, _, wc := newTestWatcher(t, 60*time.Millisecond)

	target := filepath.Join(wc.Path, "x")
	w.schedule(context.Background(), wc, target)

	// New cfg watches an unrelated directory; old path is not covered.
	other := t.TempDir()
	newCfg := &config.Config{Watches: []config.Watch{{Path: other}}}
	newFake := newFakeEngine()
	if err := w.ApplyConfig(context.Background(), newCfg, func(*config.Config) Engine { return newFake }); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Give the pending timer time to fire. The new engine should NOT see
	// the now-orphaned path; only the post-reload ScanOnce against `other`
	// should have happened.
	time.Sleep(300 * time.Millisecond)
	for _, c := range newFake.callsSnapshot() {
		if c == target {
			t.Errorf("orphaned path %s should not reach the engine after reload, got call", target)
		}
	}
}

// End-to-end: with a running Watcher.Run(), ApplyConfig adds a new watch
// path; a file created there afterwards fires HandleEntry on the new
// engine via real fsnotify.
func TestApplyConfigAddsWatchPathEndToEnd(t *testing.T) {
	dirA := t.TempDir()
	cfg := &config.Config{Watches: []config.Watch{{Path: dirA}}}
	fakeA := newFakeEngine()
	w := New(cfg, fakeA, testLogger())
	w.debounce = 40 * time.Millisecond

	cancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer cancel()

	// Let initial scan settle.
	time.Sleep(150 * time.Millisecond)

	// Reload to add dirB.
	dirB := t.TempDir()
	newCfg := &config.Config{Watches: []config.Watch{
		{Path: dirA},
		{Path: dirB},
	}}
	fakeB := newFakeEngine()
	if err := w.ApplyConfig(context.Background(), newCfg, func(*config.Config) Engine { return fakeB }); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Give fsnotify a moment to register the new dir.
	time.Sleep(100 * time.Millisecond)

	// File created in dirB should reach the new engine.
	target := filepath.Join(dirB, "hello.pdf")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		for _, c := range fakeB.callsSnapshot() {
			if c == target {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("HandleEntry for %s never reached new engine; saw %v", target, fakeB.callsSnapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// End-to-end: ApplyConfig removes a previously-watched path; subsequent
// file activity in that path must NOT reach the new engine.
func TestApplyConfigRemovesWatchPathEndToEnd(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	cfg := &config.Config{Watches: []config.Watch{
		{Path: dirA},
		{Path: dirB},
	}}
	fakeOld := newFakeEngine()
	w := New(cfg, fakeOld, testLogger())
	w.debounce = 40 * time.Millisecond

	cancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer cancel()

	time.Sleep(150 * time.Millisecond)

	// Reload, dropping dirB.
	newCfg := &config.Config{Watches: []config.Watch{{Path: dirA}}}
	fakeNew := newFakeEngine()
	if err := w.ApplyConfig(context.Background(), newCfg, func(*config.Config) Engine { return fakeNew }); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Baseline calls from the post-reload ScanOnce only (empty dir, so none
	// expected, but record just in case).
	baseline := len(fakeNew.callsSnapshot())

	// Touch a file in the now-unwatched dirB.
	orphan := filepath.Join(dirB, "stranded.pdf")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Give any (incorrect) debounce a chance to fire.
	time.Sleep(300 * time.Millisecond)
	for _, c := range fakeNew.callsSnapshot()[baseline:] {
		if c == orphan {
			t.Errorf("dropped watch should not fire HandleEntry, got call for %s", orphan)
		}
	}
}

// End-to-end smoke: full reload pipeline using config.Load + ApplyConfig +
// rules.New, including a rule that didn't exist in the original config.
//
// Demonstrates the user's headline scenario: file is sitting unmatched,
// add a rule to the config file, daemon picks it up and sorts the file.
// We write actual YAML on disk and go through config.Load so the test also
// exercises the normalization/validation pipeline the daemon uses.
func TestApplyConfigPicksUpNewRuleViaRealEngine(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "autoshelf.yaml")

	// Initial config has a rule that matches nothing in our srcDir, so the
	// pdf file just sits there. (Validate requires at least one rule per
	// watch.)
	initialYAML := `watches:
  - path: ` + srcDir + `
    rules:
      - name: Nothing
        match:
          globs: ["*.no-such-extension"]
        destination: ` + t.TempDir() + `
`
	if err := os.WriteFile(cfgPath, []byte(initialYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("initial config load: %v", err)
	}

	engine := rules.New(cfg, testLogger())
	w := New(cfg, engine, testLogger())
	w.debounce = 30 * time.Millisecond

	cancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer cancel()

	// Drop a file into srcDir. The initial scan should NOT move it.
	stranded := filepath.Join(srcDir, "report.pdf")
	if err := os.WriteFile(stranded, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(stranded); err != nil {
		t.Fatalf("file should still exist in srcDir before reload, got %v", err)
	}

	// Rewrite config with a PDF rule pointing at dstDir, then ApplyConfig.
	newYAML := `watches:
  - path: ` + srcDir + `
    rules:
      - name: PDFs
        match:
          globs: ["*.pdf"]
        destination: ` + dstDir + `
`
	if err := os.WriteFile(cfgPath, []byte(newYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	newCfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("new config load: %v", err)
	}

	if err := w.ApplyConfig(context.Background(), newCfg, func(c *config.Config) Engine {
		return rules.New(c, testLogger())
	}); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	expected := filepath.Join(dstDir, "report.pdf")
	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(expected); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected file at %s after reload-scan, never appeared", expected)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Errorf("source should have been removed after move, got %v", err)
	}
}

// Full reload pipeline: WatchConfigFile observes a file edit, calls our
// onChange which loads the new YAML and ApplyConfigs it, and the engine
// receives matched entries that the original config wouldn't have moved.
// This is the integration the daemon (cmd/run.go) actually performs.
func TestEndToEndReloadOnConfigEdit(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "autoshelf.yaml")

	initialYAML := `watches:
  - path: ` + srcDir + `
    rules:
      - name: Nothing
        match:
          globs: ["*.no-such-extension"]
        destination: ` + t.TempDir() + `
`
	if err := os.WriteFile(cfgPath, []byte(initialYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	engine := rules.New(cfg, testLogger())
	w := New(cfg, engine, testLogger())
	w.debounce = 30 * time.Millisecond

	wCancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer wCancel()

	// Drop the would-be-moved file before reload; it should sit untouched.
	stranded := filepath.Join(srcDir, "report.pdf")
	if err := os.WriteFile(stranded, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(stranded); err != nil {
		t.Fatalf("file should still be in srcDir before reload, got %v", err)
	}

	// Start the config-file watcher with the same onChange callback shape
	// that cmd/run.go uses.
	cfgCtx, cfgCancel := context.WithCancel(context.Background())
	cfgDone := make(chan struct{})
	go func() {
		defer close(cfgDone)
		_ = WatchConfigFile(cfgCtx, cfgPath, 80*time.Millisecond, func() {
			newCfg, err := config.Load(cfgPath)
			if err != nil {
				t.Logf("reload load error (ignored): %v", err)
				return
			}
			if err := w.ApplyConfig(cfgCtx, newCfg, func(c *config.Config) Engine {
				return rules.New(c, testLogger())
			}); err != nil {
				t.Logf("ApplyConfig error (ignored): %v", err)
			}
		}, testLogger())
	}()
	defer func() {
		cfgCancel()
		select {
		case <-cfgDone:
		case <-time.After(2 * time.Second):
			t.Error("config watcher did not exit")
		}
	}()

	// Give the config watcher a moment to arm.
	time.Sleep(100 * time.Millisecond)

	// Rewrite config to add a matching rule.
	newYAML := `watches:
  - path: ` + srcDir + `
    rules:
      - name: PDFs
        match:
          globs: ["*.pdf"]
        destination: ` + dstDir + `
`
	if err := os.WriteFile(cfgPath, []byte(newYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// The file should land at dstDir after debounce + ApplyConfig + ScanOnce.
	expected := filepath.Join(dstDir, "report.pdf")
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(expected); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("file never appeared at %s after edit-driven reload", expected)
		case <-time.After(30 * time.Millisecond):
		}
	}
}

// Invalid YAML written to the config file must NOT take down the daemon
// or wipe its current rules. The reload pipeline should log and skip.
func TestEndToEndReloadIgnoresInvalidYAML(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "autoshelf.yaml")

	goodYAML := `watches:
  - path: ` + srcDir + `
    rules:
      - name: PDFs
        match:
          globs: ["*.pdf"]
        destination: ` + dstDir + `
`
	if err := os.WriteFile(cfgPath, []byte(goodYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	engine := rules.New(cfg, testLogger())
	w := New(cfg, engine, testLogger())
	w.debounce = 30 * time.Millisecond

	wCancel, waitExit := startWatcher(t, w)
	defer waitExit()
	defer wCancel()

	var reloadAttempts atomic.Int32
	cfgCtx, cfgCancel := context.WithCancel(context.Background())
	cfgDone := make(chan struct{})
	go func() {
		defer close(cfgDone)
		_ = WatchConfigFile(cfgCtx, cfgPath, 60*time.Millisecond, func() {
			reloadAttempts.Add(1)
			newCfg, err := config.Load(cfgPath)
			if err != nil {
				// expected for the invalid-YAML write below
				return
			}
			_ = w.ApplyConfig(cfgCtx, newCfg, func(c *config.Config) Engine {
				return rules.New(c, testLogger())
			})
		}, testLogger())
	}()
	defer func() {
		cfgCancel()
		<-cfgDone
	}()

	time.Sleep(80 * time.Millisecond)

	// Write garbage.
	if err := os.WriteFile(cfgPath, []byte("not: valid: yaml: [unterminated"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Wait for the reload attempt to happen and fail.
	deadline := time.After(2 * time.Second)
	for reloadAttempts.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("reload callback never fired")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Daemon should still respond to new files via the ORIGINAL rule -
	// drop a pdf in srcDir and verify it lands in dstDir.
	stranded := filepath.Join(srcDir, "afterbad.pdf")
	if err := os.WriteFile(stranded, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(dstDir, "afterbad.pdf")
	deadline = time.After(2 * time.Second)
	for {
		if _, err := os.Stat(expected); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("file never moved by surviving original rule (expected %s)", expected)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Concurrent ApplyConfig and HandleEntry must not race or panic. Sets the
// race detector loose on the swap path.
func TestApplyConfigConcurrentWithSchedule(t *testing.T) {
	w, _, wc := newTestWatcher(t, 1*time.Millisecond)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	// Scheduler: hammer events through schedule().
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				p := filepath.Join(wc.Path, "f-"+time.Now().Format("150405.000000")+"-"+string(rune('a'+(i%26))))
				w.schedule(context.Background(), wc, p)
				i++
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	// Reloader: swap configs as fast as it can.
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			select {
			case <-stop:
				return
			default:
			}
			newCfg := &config.Config{Watches: []config.Watch{{Path: wc.Path}}}
			_ = w.ApplyConfig(context.Background(), newCfg, func(*config.Config) Engine { return newFakeEngine() })
			time.Sleep(2 * time.Millisecond)
		}
	}()

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}

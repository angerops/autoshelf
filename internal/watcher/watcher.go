package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/angerops/autoshelf/internal/config"
	"github.com/angerops/autoshelf/internal/rules"
)

// Debounce window between an event firing and the file being inspected. Gives
// the OS time to finish writing - we'd otherwise race with the writer and
// move a half-flushed file.
const defaultDebounce = 500 * time.Millisecond

// Engine is the subset of rule-engine behavior the watcher depends on.
// Declared here as an interface so tests can substitute a counting fake
// without pulling in the real rule machinery. *rules.Engine satisfies it.
type Engine interface {
	// HandleEntry processes a single path. Returns applied=true on move;
	// a non-zero retryAt when a rule matched but the entry has not yet
	// satisfied min_age (the caller should re-call at retryAt).
	HandleEntry(w *config.Watch, path string) (applied bool, retryAt time.Time, err error)
	ScanOnce() (matched int, scanned int, deferred []rules.Deferred, err error)
}

// Watcher monitors configured folders and dispatches files to the rule engine.
//
// cfg, engine, fsw, and dirToWatch are all guarded by mu because ApplyConfig
// may swap them from a separate goroutine while the main event loop or a
// debounce timer reads them. mu also guards pending. Lock hold times are
// short (map lookups, pointer swaps, fsnotify Add/Remove calls).
type Watcher struct {
	logger   *slog.Logger
	debounce time.Duration

	mu          sync.Mutex
	cfg         *config.Config
	engine      Engine
	fsw         *fsnotify.Watcher
	dirToWatch  map[string]*config.Watch
	pending     map[string]*time.Timer
}

// New constructs a Watcher. The logger may be nil.
func New(cfg *config.Config, engine Engine, logger *slog.Logger) *Watcher {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Watcher{
		cfg:        cfg,
		engine:     engine,
		logger:     logger,
		debounce:   defaultDebounce,
		dirToWatch: map[string]*config.Watch{},
		pending:    map[string]*time.Timer{},
	}
}

// Run starts watching and blocks until ctx is cancelled. It performs an
// initial scan of every watched folder so existing files get sorted before
// real-time events take over.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	w.mu.Lock()
	w.fsw = fsw
	w.mu.Unlock()

	for i := range w.cfg.Watches {
		wc := &w.cfg.Watches[i]
		dirs, err := collectDirs(wc.Path, wc.Recursive)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			if err := fsw.Add(d); err != nil {
				return err
			}
			w.mu.Lock()
			w.dirToWatch[d] = wc
			w.mu.Unlock()
			w.logger.Info("watching", "path", d)
		}
	}

	// Initial scan picks up files that were already sitting in the folder
	// when the daemon started. Anything deferred by min_age is re-queued
	// here so it'll be re-evaluated once eligible, even without a new event.
	go w.runInitialScan(ctx, "initial scan complete")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.logger.Error("watcher error", "err", err)

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			w.handleEvent(ctx, ev)
		}
	}
}

// runInitialScan runs ScanOnce against the engine currently bound to the
// watcher and re-queues any deferred entries at their retryAt. Used both at
// startup and after ApplyConfig adds new watch paths so existing files there
// get sorted promptly.
func (w *Watcher) runInitialScan(ctx context.Context, label string) {
	w.mu.Lock()
	engine := w.engine
	w.mu.Unlock()

	matched, scanned, deferred, err := engine.ScanOnce()
	if err != nil {
		w.logger.Error("scan failed", "label", label, "err", err)
		return
	}
	w.logger.Info(label,
		"scanned", scanned,
		"matched", matched,
		"deferred", len(deferred))
	for _, d := range deferred {
		wc := w.watchFor(d.Path)
		if wc == nil {
			continue
		}
		w.scheduleAt(ctx, wc, d.Path, d.RetryAt)
	}
}

func (w *Watcher) handleEvent(ctx context.Context, ev fsnotify.Event) {
	// Only act on creates and writes. Removes/renames may refer to files we
	// just moved ourselves, and chmods are irrelevant.
	if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Write) {
		return
	}

	w.mu.Lock()
	wc, ok := w.dirToWatch[filepath.Dir(ev.Name)]
	fsw := w.fsw
	w.mu.Unlock()
	if !ok {
		return
	}

	// If a new subdirectory appeared inside a recursive watch, register it
	// with fsnotify so its contents will be observed. We do NOT early-return:
	// a dir-kind rule may still want to move this directory, so the entry
	// still flows through to schedule() below. If the rule moves it, the
	// (now stale) fsnotify registration becomes a no-op.
	if wc.Recursive && fsw != nil {
		if info, err := os.Lstat(ev.Name); err == nil && info.IsDir() {
			if err := fsw.Add(ev.Name); err != nil {
				w.logger.Warn("could not add new subdir", "path", ev.Name, "err", err)
			} else {
				w.mu.Lock()
				w.dirToWatch[ev.Name] = wc
				w.mu.Unlock()
				w.logger.Info("watching new subdir", "path", ev.Name)
			}
		}
	}

	w.schedule(ctx, wc, ev.Name)
}

// schedule queues the entry for evaluation after the debounce window.
// Repeated events for the same path collapse into one HandleEntry call.
func (w *Watcher) schedule(ctx context.Context, wc *config.Watch, path string) {
	w.scheduleAt(ctx, wc, path, time.Now().Add(w.debounce))
}

// scheduleAt queues HandleEntry for path to run at (or as close as possible
// to) the given absolute time. Used by both the debounce path (now+debounce)
// and the min_age deferral path (mtime+min_age). Existing timers are reset
// to fire at the new time.
func (w *Watcher) scheduleAt(ctx context.Context, wc *config.Watch, path string, at time.Time) {
	delay := time.Until(at)
	if delay < 0 {
		delay = 0
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if existing, ok := w.pending[path]; ok {
		existing.Reset(delay)
		return
	}
	w.pending[path] = time.AfterFunc(delay, func() {
		w.runHandle(ctx, wc, path)
	})
}

// runHandle is the timer callback: pop the pending entry, call HandleEntry,
// and (if the engine asks) re-queue at retryAt.
//
// The watch hint wcHint comes from whichever call scheduled this entry. If
// config has been reloaded since the timer was set, the hint may point at a
// stale Watch struct from an old cfg generation, so we always re-resolve the
// current watch by path before calling HandleEntry. The engine pointer is
// likewise snapshotted under the lock so ApplyConfig's atomic-feeling swap
// doesn't race a fire.
func (w *Watcher) runHandle(ctx context.Context, wcHint *config.Watch, path string) {
	w.mu.Lock()
	delete(w.pending, path)
	engine := w.engine
	current := w.watchForLocked(path)
	w.mu.Unlock()

	if ctx.Err() != nil {
		return
	}
	if current == nil {
		// Path no longer covered by any configured watch (e.g. the watch was
		// removed by a config reload). Drop the event quietly.
		return
	}
	_ = wcHint // kept for backward-compat with internal callers; current wins
	applied, retryAt, err := engine.HandleEntry(current, path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			w.logger.Error("handle entry failed", "path", path, "err", err)
		}
		return
	}
	if !applied && !retryAt.IsZero() {
		w.scheduleAt(ctx, current, path, retryAt)
	}
}

// watchFor returns the Watch entry that owns the given absolute path, or
// nil if no configured watch contains it. Used to route deferred entries
// from the initial scan back to their watch. Acquires w.mu.
func (w *Watcher) watchFor(path string) *config.Watch {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.watchForLocked(path)
}

// watchForLocked is the inner form; the caller must hold w.mu.
func (w *Watcher) watchForLocked(path string) *config.Watch {
	cleaned := filepath.Clean(path)
	var best *config.Watch
	bestLen := -1
	for i := range w.cfg.Watches {
		wc := &w.cfg.Watches[i]
		root := filepath.Clean(wc.Path)
		if cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
			// Pick the longest matching root in case watches nest.
			if len(root) > bestLen {
				best = wc
				bestLen = len(root)
			}
		}
	}
	return best
}

// ApplyConfig reconciles the running watcher to a new config. It is safe to
// call from a separate goroutine while Run is executing.
//
// Reconciliation steps:
//  1. Resolve the set of directories the new cfg wants watched (honoring
//     each watch's recursive flag).
//  2. Add fsnotify watches for any directory not already watched; drop
//     fsnotify watches for any that are no longer wanted.
//  3. Swap the engine for one built from the new cfg (so protected
//     destinations, rules, and dry-run settings all reflect the new file).
//  4. Kick off a ScanOnce so any pre-existing files in newly-added paths -
//     or files that the old ruleset didn't match but the new one does - get
//     evaluated immediately rather than waiting for the next event.
//
// engineFactory builds the replacement engine. We accept it as a callback so
// internal/watcher doesn't need to import internal/rules (the constructor is
// provided by cmd/run.go, which already wires the two together).
//
// Errors building the new directory set are returned without mutating state.
// Errors from individual fsw.Add / fsw.Remove calls are logged and skipped:
// a single broken path shouldn't prevent the rest of the reconciliation.
func (w *Watcher) ApplyConfig(ctx context.Context, newCfg *config.Config, engineFactory func(*config.Config) Engine) error {
	if newCfg == nil {
		return errors.New("ApplyConfig: nil config")
	}
	if engineFactory == nil {
		return errors.New("ApplyConfig: nil engineFactory")
	}

	// Build the desired dir -> watch map from the new cfg before touching
	// any state, so a discovery error doesn't leave us half-reconciled.
	desired := map[string]*config.Watch{}
	for i := range newCfg.Watches {
		wc := &newCfg.Watches[i]
		dirs, err := collectDirs(wc.Path, wc.Recursive)
		if err != nil {
			return fmt.Errorf("ApplyConfig: %w", err)
		}
		for _, d := range dirs {
			desired[d] = wc
		}
	}

	newEngine := engineFactory(newCfg)

	w.mu.Lock()
	fsw := w.fsw
	current := w.dirToWatch
	w.cfg = newCfg
	w.engine = newEngine
	w.dirToWatch = desired
	w.mu.Unlock()

	if fsw != nil {
		for d := range desired {
			if _, already := current[d]; already {
				continue
			}
			if err := fsw.Add(d); err != nil {
				w.logger.Warn("ApplyConfig: could not add watch", "path", d, "err", err)
				continue
			}
			w.logger.Info("watching", "path", d)
		}
		for d := range current {
			if _, keep := desired[d]; keep {
				continue
			}
			if err := fsw.Remove(d); err != nil {
				w.logger.Warn("ApplyConfig: could not remove watch", "path", d, "err", err)
				continue
			}
			w.logger.Info("unwatching", "path", d)
		}
	}

	// Always sweep on reload so rule changes apply to existing files, not
	// just future events. Matches the user's mental model of "I edited the
	// config; the new rules should take effect now."
	go w.runInitialScan(ctx, "post-reload scan complete")

	return nil
}

// collectDirs returns the directories to register with fsnotify. With
// recursive=false, only root is returned; otherwise root and all descendants.
func collectDirs(root string, recursive bool) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &os.PathError{Op: "watch", Path: root, Err: errors.New("not a directory")}
	}
	if !recursive {
		return []string{root}, nil
	}
	var dirs []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

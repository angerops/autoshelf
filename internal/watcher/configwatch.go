package watcher

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// configWatchDebounce coalesces the burst of events most editors emit on save
// (truncate+write, or temp-write+rename). Long enough to absorb a stutter,
// short enough that a human editing the file feels the reload as instant.
const configWatchDebounce = 250 * time.Millisecond

// WatchConfigFile watches a single config file for changes and invokes
// onChange (debounced) whenever the file is created, written, or renamed
// into existence at that path.
//
// We watch the *parent directory* rather than the file itself because most
// editors don't write in place: vim writes to a temp file and renames it
// over the target, and VS Code does similar atomic-replace dances. A watch
// bound to the file's inode would die on the rename. A directory watch
// catches Create/Rename events for any child including the one we care
// about.
//
// onChange runs in this function's goroutine; do not block on it for long.
//
// Blocks until ctx is cancelled. Returns ctx.Err() (or nil if ctx was nil).
//
// logger may be nil; a default text logger to stderr is used.
func WatchConfigFile(ctx context.Context, path string, debounce time.Duration, onChange func(), logger *slog.Logger) error {
	if path == "" {
		return errors.New("WatchConfigFile: empty path")
	}
	if onChange == nil {
		return errors.New("WatchConfigFile: nil onChange")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if debounce <= 0 {
		debounce = configWatchDebounce
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(absPath)
	target := filepath.Base(absPath)

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	if err := fsw.Add(parent); err != nil {
		return err
	}

	// Single AfterFunc timer reused for the debounce window. We Reset() it
	// on every relevant event; the callback fires once when activity stops.
	var timer *time.Timer
	timer = time.AfterFunc(debounce, func() {
		// Re-check context at fire time: if cancel raced the timer expiry
		// we don't want to invoke onChange after the caller has moved on.
		if ctx.Err() != nil {
			return
		}
		onChange()
	})
	timer.Stop() // start armed but not running; events Reset it

	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			logger.Warn("config watch error", "err", err)

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if !configEventMatches(ev, target) {
				continue
			}
			timer.Reset(debounce)
		}
	}
}

// configEventMatches reports whether an event in the parent directory is one
// we should treat as "the config file was just (re)written". We accept
// Create/Write/Rename on the target basename. Chmod is ignored - touching
// permissions shouldn't trigger a reload. Remove without a follow-up
// Create/Rename is also ignored: the daemon should keep running the old
// config rather than blow up because the file briefly didn't exist.
func configEventMatches(ev fsnotify.Event, target string) bool {
	if filepath.Base(ev.Name) != target {
		return false
	}
	return ev.Has(fsnotify.Create) || ev.Has(fsnotify.Write) || ev.Has(fsnotify.Rename)
}

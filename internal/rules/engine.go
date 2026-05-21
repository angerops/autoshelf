package rules

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/angerops/autoshelf/internal/config"
)

// Deferred describes an entry that matched a rule but was not moved because
// the rule's min_age has not yet elapsed since the entry's mtime. Callers
// (typically the watcher) can re-call HandleEntry at RetryAt to try again.
type Deferred struct {
	Path    string
	RetryAt time.Time
}

// Engine evaluates rules against filesystem entries and performs the resulting
// moves. It tracks the set of all configured destinations so a catch-all rule
// (e.g. {globs: ["*"], kind: dir}) cannot move its own destination folder.
type Engine struct {
	cfg    *config.Config
	logger *slog.Logger

	// protectedPaths is the set of every destination across every rule. Any
	// path that exactly matches one of these is skipped during rule evaluation.
	// Destinations are already absolute and cleaned by config.normalize.
	protectedPaths map[string]struct{}
}

// ErrConflict is returned when a rule's on_conflict is "error" and the
// destination already exists. Callers can errors.Is it to react.
var ErrConflict = errors.New("destination already exists")

// New returns an Engine bound to cfg. If logger is nil a default text logger
// writing to stderr is used.
func New(cfg *config.Config, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	protected := map[string]struct{}{}
	for _, w := range cfg.Watches {
		for _, r := range w.Rules {
			if r.Destination != "" {
				protected[filepath.Clean(r.Destination)] = struct{}{}
			}
		}
	}
	return &Engine{cfg: cfg, logger: logger, protectedPaths: protected}
}

// HandleEntry evaluates rules for a single filesystem entry (file or dir)
// against the watch that produced it. Rules are tried in order; the first
// matching rule wins.
//
// Return semantics:
//   - applied=true means a rule fired and the move happened (or would have,
//     under dry-run).
//   - retryAt non-zero means a rule matched but the entry has not yet
//     existed for the rule's min_age window. The caller should re-call
//     HandleEntry at or after retryAt; until then the entry is left alone.
//   - applied=false with retryAt zero and err nil means no rule matched.
func (e *Engine) HandleEntry(w *config.Watch, path string) (applied bool, retryAt time.Time, err error) {
	// Never act on the watch root itself.
	if filepath.Clean(path) == filepath.Clean(w.Path) {
		return false, time.Time{}, nil
	}
	// Never act on a configured destination.
	if _, isProtected := e.protectedPaths[filepath.Clean(path)]; isProtected {
		return false, time.Time{}, nil
	}
	// Skip anything matching the global or per-watch ignore globs. Logged
	// at DEBUG so -v surfaces "yes I saw it, yes I ignored it" without
	// drowning normal output in .DS_Store noise.
	if e.isIgnored(w, filepath.Base(path)) {
		e.logger.Debug("ignored", "path", path, "reason", "ignore_globs")
		return false, time.Time{}, nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Entry vanished between event and handler. Not an error.
			return false, time.Time{}, nil
		}
		return false, time.Time{}, err
	}

	isDir := info.IsDir()
	isRegular := info.Mode().IsRegular()
	if !isDir && !isRegular {
		// Symlinks, devices, sockets etc. - leave them alone.
		return false, time.Time{}, nil
	}

	base := filepath.Base(path)
	for _, r := range w.Rules {
		if isDir && !r.Match.AppliesToDir() {
			continue
		}
		if isRegular && !r.Match.AppliesToFile() {
			continue
		}
		if !matchesAny(base, r.Match.Globs) {
			continue
		}
		// Rule matches. Check min_age before doing anything.
		// Precedence: rule's min_age > watch's min_age > kind-default.
		if minAge := w.EffectiveMinAge(r); minAge > 0 {
			age := time.Since(info.ModTime())
			if age < minAge {
				ra := info.ModTime().Add(minAge)
				e.logger.Info("deferred (min_age not met)",
					"rule", r.Name,
					"path", path,
					"age", age.Round(time.Second),
					"min_age", minAge,
					"retry_at", ra)
				return false, ra, nil
			}
		}
		ok, err := e.apply(r, path, isDir)
		if err != nil {
			return false, time.Time{}, err
		}
		return ok, time.Time{}, nil
	}
	return false, time.Time{}, nil
}

// ScanOnce walks every watched folder and applies rules to existing entries.
// Top-level directories are considered for matching before any descent; if a
// directory rule fires the subtree is skipped. With recursive=false, subtrees
// that did not match are skipped anyway.
//
// Entries whose matching rule has not yet satisfied its min_age are reported
// in the returned deferred slice rather than moved; callers decide whether to
// re-queue (the watcher does) or just log (the once command does).
func (e *Engine) ScanOnce() (matched int, scanned int, deferred []Deferred, err error) {
	for i := range e.cfg.Watches {
		w := &e.cfg.Watches[i]
		walkErr := filepath.WalkDir(w.Path, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				e.logger.Warn("walk error", "path", p, "err", walkErr)
				return nil
			}
			// Skip the watch root itself.
			if filepath.Clean(p) == filepath.Clean(w.Path) {
				return nil
			}
			// Protected destinations: don't descend, don't match. This is the
			// safety net for catch-all dir rules.
			if _, isProtected := e.protectedPaths[filepath.Clean(p)]; isProtected {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			// Ignored entries (browser partial downloads, OS metadata, ...)
			// are skipped here too. For directories that's important: a
			// Safari ".download" tree is actively being written to and we
			// must not traverse into it.
			if e.isIgnored(w, filepath.Base(p)) {
				e.logger.Debug("ignored", "path", p, "reason", "ignore_globs")
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			scanned++
			ok, retryAt, err := e.HandleEntry(w, p)
			if err != nil {
				e.logger.Error("rule error", "path", p, "err", err)
				return nil
			}
			if ok {
				matched++
				if d.IsDir() {
					// Already moved, don't try to recurse into the now-gone path.
					return filepath.SkipDir
				}
				return nil
			}
			if !retryAt.IsZero() {
				deferred = append(deferred, Deferred{Path: p, RetryAt: retryAt})
				// A deferred dir still exists - don't descend into it under
				// a non-recursive watch; under recursive we want to keep
				// walking in case something inside also matches.
				if d.IsDir() && !w.Recursive {
					return filepath.SkipDir
				}
				return nil
			}
			// Did not match. Honor non-recursive mode by skipping subtrees.
			if d.IsDir() && !w.Recursive {
				return filepath.SkipDir
			}
			return nil
		})
		if walkErr != nil {
			return matched, scanned, deferred, walkErr
		}
	}
	return matched, scanned, deferred, nil
}

// apply performs the rule's move on src. Honors dry-run and on_conflict.
// Returns true when the move was performed (or would have been under dry-run),
// false when the rule deliberately skipped (on_conflict=skip).
func (e *Engine) apply(r config.Rule, src string, isDir bool) (bool, error) {
	dst := filepath.Join(r.Destination, filepath.Base(src))

	// Resolve any name collision according to the rule's policy.
	final, action, err := e.resolveConflict(r, dst)
	if err != nil {
		return false, err
	}
	if action == actionSkip {
		e.logger.Info("skip (conflict)", "rule", r.Name, "src", src, "dst", dst)
		return false, nil
	}

	if e.cfg.DryRun {
		e.logger.Info("dry-run move", "rule", r.Name, "src", src, "dst", final, "kind", entryKind(isDir), "action", string(action))
		return true, nil
	}

	if err := os.MkdirAll(r.Destination, 0o755); err != nil {
		return false, err
	}

	if action == actionOverwrite {
		if err := os.RemoveAll(final); err != nil {
			return false, err
		}
	}

	if isDir {
		if err := moveDir(src, final); err != nil {
			return false, err
		}
	} else {
		if err := moveFile(src, final); err != nil {
			return false, err
		}
	}

	e.logger.Info("moved", "rule", r.Name, "src", src, "dst", final, "kind", entryKind(isDir), "action", string(action))
	return true, nil
}

// conflictAction is the resolved decision after consulting on_conflict policy.
type conflictAction string

const (
	actionMove      conflictAction = "move"      // no collision, plain move
	actionRename    conflictAction = "rename"    // collision, used a suffixed name
	actionOverwrite conflictAction = "overwrite" // collision, replace destination
	actionSkip      conflictAction = "skip"      // collision, leave source alone
)

// resolveConflict applies the rule's OnConflict policy to a proposed
// destination. Returns the final destination path and the action that was
// chosen. If the policy is "error" and the destination exists, ErrConflict is
// returned.
func (e *Engine) resolveConflict(r config.Rule, dst string) (string, conflictAction, error) {
	exists, err := pathExists(dst)
	if err != nil {
		return "", "", err
	}
	if !exists {
		return dst, actionMove, nil
	}
	switch r.OnConflict {
	case config.ConflictRename, "":
		final, err := uniqueDestination(dst)
		if err != nil {
			return "", "", err
		}
		return final, actionRename, nil
	case config.ConflictSkip:
		return dst, actionSkip, nil
	case config.ConflictOverwrite:
		return dst, actionOverwrite, nil
	case config.ConflictError:
		return "", "", fmt.Errorf("rule %q: %w: %s", r.Name, ErrConflict, dst)
	default:
		return "", "", fmt.Errorf("rule %q: unknown on_conflict %q", r.Name, r.OnConflict)
	}
}

// isIgnored returns true if the given base name matches the combined global
// and per-watch ignore_globs lists. Matching uses the same case-insensitive
// semantics as rule matching.
func (e *Engine) isIgnored(w *config.Watch, base string) bool {
	ignores := e.cfg.EffectiveIgnoreGlobs(w)
	if len(ignores) == 0 {
		return false
	}
	return matchesAny(base, ignores)
}

// matchesAny returns true if name matches any of the supplied glob patterns.
// Matching is case-insensitive against the file's base name.
func matchesAny(name string, globs []string) bool {
	lower := strings.ToLower(name)
	for _, g := range globs {
		ok, err := filepath.Match(strings.ToLower(g), lower)
		if err != nil {
			// Validate() should have caught this; treat as non-match.
			continue
		}
		if ok {
			return true
		}
	}
	return false
}

// uniqueDestination returns dst, or a numerically suffixed variant if dst
// already exists. e.g. report.pdf -> report (1).pdf. For directories the
// suffix is appended to the whole base name (no extension split).
func uniqueDestination(dst string) (string, error) {
	exists, err := pathExists(dst)
	if err != nil {
		return "", err
	}
	if !exists {
		return dst, nil
	}

	dir := filepath.Dir(dst)
	base := filepath.Base(dst)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	// For directories there's no meaningful extension; ext will usually be
	// empty, in which case stem == base.

	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		exists, err := pathExists(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find unique destination after 9999 tries for %s", dst)
}

func pathExists(p string) (bool, error) {
	if _, err := os.Lstat(p); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, err
	}
}

func entryKind(isDir bool) string {
	if isDir {
		return "dir"
	}
	return "file"
}

// moveFile renames src -> dst, falling back to copy+remove across devices.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// moveDir renames a directory tree, falling back to recursive copy+remove
// across devices. Note: copy is best-effort on permissions and timestamps -
// the goal is "no data lost," not "byte-for-byte preserved."
func moveDir(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return err
	}
	if err := copyDir(src, dst); err != nil {
		// Roll back partial copy so we don't leave half a tree behind.
		_ = os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	srcInfo, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, srcInfo.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// copyDir recursively copies src directory tree to dst. dst must not exist.
// Symlinks are recreated as symlinks; regular files are copied.
func copyDir(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("copyDir: %s is not a directory", src)
	}
	if err := os.MkdirAll(dst, srcInfo.Mode().Perm()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		info, err := entry.Info()
		if err != nil {
			return err
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
		case info.IsDir():
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		default:
			// Skip devices, sockets, pipes - shouldn't appear in a Downloads
			// folder, and we don't want to silently lose them either, so log.
			return fmt.Errorf("copyDir: refusing to copy non-regular non-dir non-symlink entry: %s", srcPath)
		}
	}
	return nil
}

// isCrossDevice reports whether err is the EXDEV "invalid cross-device link"
// returned by rename() when src and dst live on different filesystems.
func isCrossDevice(err error) bool {
	if err == nil {
		return false
	}
	// syscall.EXDEV would be cleaner, but pulling in syscall just for this
	// is unnecessary - the error string is stable.
	return strings.Contains(err.Error(), "cross-device")
}

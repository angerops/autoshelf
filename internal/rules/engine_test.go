package rules

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/angerops/autoshelf/internal/config"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func TestMatchesAnyCaseInsensitive(t *testing.T) {
	cases := []struct {
		name  string
		globs []string
		want  bool
	}{
		{"report.PDF", []string{"*.pdf"}, true},
		{"IMG_1234.HEIC", []string{"img_*.heic"}, true},
		{"notes.txt", []string{"*.pdf", "*.docx"}, false},
		{"Screen Shot 2026-05-19.png", []string{"screen shot *.png"}, true},
	}
	for _, tc := range cases {
		got := matchesAny(tc.name, tc.globs)
		if got != tc.want {
			t.Errorf("matchesAny(%q, %v) = %v, want %v", tc.name, tc.globs, got, tc.want)
		}
	}
}

func TestUniqueDestinationCollision(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := uniqueDestination(target)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "report (1).pdf")
	if got != want {
		t.Errorf("uniqueDestination collision: got %q want %q", got, want)
	}
}

func TestUniqueDestinationNoCollision(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new.pdf")
	got, err := uniqueDestination(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("uniqueDestination should return original when free: got %q want %q", got, target)
	}
}

// fileEngine builds a one-watch, one-rule engine for file tests.
func fileEngine(t *testing.T, src, dst string, onConflict string) (*Engine, *config.Watch) {
	t.Helper()
	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "PDFs",
				Match:       config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  onConflict,
			}},
		}},
	}
	return New(cfg, newTestLogger()), &cfg.Watches[0]
}

func TestHandleEntryMovesAndCreatesDir(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "PDFs") // intentionally non-existent

	srcFile := filepath.Join(src, "invoice.pdf")
	if err := os.WriteFile(srcFile, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, w := fileEngine(t, src, dst, config.ConflictRename)
	ok, _, err := e.HandleEntry(w, srcFile)
	if err != nil || !ok {
		t.Fatalf("expected rule to match: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Errorf("source file should be gone after move, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "invoice.pdf")); err != nil {
		t.Errorf("expected file in destination: %v", err)
	}
}

func TestHandleEntryDryRunDoesNotMove(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "PDFs")

	srcFile := filepath.Join(src, "invoice.pdf")
	if err := os.WriteFile(srcFile, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		DryRun: true,
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "PDFs",
				Match:       config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	ok, _, err := e.HandleEntry(&cfg.Watches[0], srcFile)
	if err != nil || !ok {
		t.Fatalf("expected rule to match: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(srcFile); err != nil {
		t.Errorf("dry-run should leave source in place: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("dry-run should not create destination dir")
	}
}

func TestHandleEntryFirstRuleWins(t *testing.T) {
	src := t.TempDir()
	dst1 := filepath.Join(t.TempDir(), "first")
	dst2 := filepath.Join(t.TempDir(), "second")

	srcFile := filepath.Join(src, "thing.pdf")
	if err := os.WriteFile(srcFile, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{
				{Name: "first", Match: config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile}, Destination: dst1, OnConflict: config.ConflictRename},
				{Name: "second", Match: config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile}, Destination: dst2, OnConflict: config.ConflictRename},
			},
		}},
	}
	e := New(cfg, newTestLogger())

	if _, _, err := e.HandleEntry(&cfg.Watches[0], srcFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst1, "thing.pdf")); err != nil {
		t.Errorf("first rule should have won: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst2, "thing.pdf")); !os.IsNotExist(err) {
		t.Errorf("second rule should not have applied")
	}
}

func TestOnConflictRenameSuffixes(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Pre-existing file at destination with the same name.
	if err := os.WriteFile(filepath.Join(dst, "invoice.pdf"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(src, "invoice.pdf")
	if err := os.WriteFile(srcFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, w := fileEngine(t, src, dst, config.ConflictRename)
	if _, _, err := e.HandleEntry(w, srcFile); err != nil {
		t.Fatal(err)
	}
	// Original at dst stayed put, new file took the suffix.
	old, _ := os.ReadFile(filepath.Join(dst, "invoice.pdf"))
	if string(old) != "old" {
		t.Errorf("original destination file was clobbered: %q", old)
	}
	newFile, err := os.ReadFile(filepath.Join(dst, "invoice (1).pdf"))
	if err != nil {
		t.Fatalf("expected suffixed file: %v", err)
	}
	if string(newFile) != "new" {
		t.Errorf("suffixed file has wrong content: %q", newFile)
	}
}

func TestOnConflictSkipLeavesSourceAlone(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(dst, "invoice.pdf"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(src, "invoice.pdf")
	if err := os.WriteFile(srcFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, w := fileEngine(t, src, dst, config.ConflictSkip)
	ok, _, err := e.HandleEntry(w, srcFile)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("skip should report not-applied")
	}
	if _, err := os.Stat(srcFile); err != nil {
		t.Errorf("source should still exist after skip: %v", err)
	}
	old, _ := os.ReadFile(filepath.Join(dst, "invoice.pdf"))
	if string(old) != "old" {
		t.Errorf("destination should be untouched, got %q", old)
	}
}

func TestOnConflictErrorReturnsErrConflict(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(dst, "invoice.pdf"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(src, "invoice.pdf")
	if err := os.WriteFile(srcFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, w := fileEngine(t, src, dst, config.ConflictError)
	_, _, err := e.HandleEntry(w, srcFile)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestOnConflictOverwriteReplacesDestination(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(dst, "invoice.pdf"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(src, "invoice.pdf")
	if err := os.WriteFile(srcFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, w := fileEngine(t, src, dst, config.ConflictOverwrite)
	if _, _, err := e.HandleEntry(w, srcFile); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "invoice.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("expected overwrite, got %q", got)
	}
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Errorf("source should be gone after overwrite")
	}
}

func TestDirKindMovesDirectoryTree(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "Loose")

	// Build a small tree under src: src/stuff/inner/file.txt
	loose := filepath.Join(src, "stuff")
	inner := filepath.Join(loose, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "Loose folders",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindDir},
				Destination: dst,
				OnConflict:  config.ConflictRename,
				MinAge:      "0s", // disable the 5m dir default for this test
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	ok, _, err := e.HandleEntry(&cfg.Watches[0], loose)
	if err != nil || !ok {
		t.Fatalf("expected dir rule to match: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(loose); !os.IsNotExist(err) {
		t.Errorf("source dir should be gone after move")
	}
	movedFile := filepath.Join(dst, "stuff", "inner", "file.txt")
	if _, err := os.Stat(movedFile); err != nil {
		t.Errorf("expected file inside moved tree: %v", err)
	}
}

func TestProtectedDestinationsNotMovedByCatchAll(t *testing.T) {
	src := t.TempDir()
	// Both destinations are inside src - this is the realistic case (Downloads).
	pdfDest := filepath.Join(src, "PDFs")
	looseDest := filepath.Join(src, "Loose")

	if err := os.MkdirAll(pdfDest, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{
				{
					Name:        "PDFs",
					Match:       config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile},
					Destination: pdfDest,
					OnConflict:  config.ConflictRename,
				},
				{
					Name:        "Loose folders",
					Match:       config.Match{Globs: []string{"*"}, Kind: config.KindDir},
					Destination: looseDest,
					OnConflict:  config.ConflictRename,
					MinAge:      "0s", // disable the 5m default for this protection test
				},
			},
		}},
	}
	e := New(cfg, newTestLogger())

	// The "PDFs" dir would match the catch-all dir rule by glob, but it's a
	// destination of another rule and must be protected.
	ok, _, err := e.HandleEntry(&cfg.Watches[0], pdfDest)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("PDFs destination should not have been moved by catch-all")
	}
	if _, err := os.Stat(pdfDest); err != nil {
		t.Errorf("PDFs destination should still exist: %v", err)
	}

	// The catch-all dir rule's own destination must also be protected.
	if err := os.MkdirAll(looseDest, 0o755); err != nil {
		t.Fatal(err)
	}
	ok, _, err = e.HandleEntry(&cfg.Watches[0], looseDest)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("Loose destination should not move itself")
	}
}

func TestScanOnceSweepsLooseDirs(t *testing.T) {
	src := t.TempDir()
	pdfDest := filepath.Join(src, "PDFs")
	looseDest := filepath.Join(src, "Loose")

	// Pre-populate: one stray dir, one PDF, plus the PDFs destination.
	if err := os.MkdirAll(pdfDest, 0o755); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(src, "random project")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "doc.pdf"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path:      src,
			Recursive: false,
			Rules: []config.Rule{
				{Name: "PDFs", Match: config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile}, Destination: pdfDest, OnConflict: config.ConflictRename},
				{Name: "Loose folders", Match: config.Match{Globs: []string{"*"}, Kind: config.KindDir}, Destination: looseDest, OnConflict: config.ConflictRename, MinAge: "0s"},
			},
		}},
	}
	e := New(cfg, newTestLogger())

	matched, _, _, err := e.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if matched < 2 {
		t.Errorf("expected at least 2 matches (pdf + stray dir), got %d", matched)
	}
	if _, err := os.Stat(filepath.Join(pdfDest, "doc.pdf")); err != nil {
		t.Errorf("pdf should have been moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(looseDest, "random project", "notes.md")); err != nil {
		t.Errorf("stray dir tree should have been moved: %v", err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("original stray dir should be gone")
	}
	// PDFs destination is still in place, untouched.
	if _, err := os.Stat(pdfDest); err != nil {
		t.Errorf("PDFs destination should be preserved: %v", err)
	}
}

func TestSymlinksAreSkipped(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	target := filepath.Join(src, "real.pdf")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(src, "link.pdf")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	e, w := fileEngine(t, src, dst, config.ConflictRename)
	ok, _, err := e.HandleEntry(w, link)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("symlinks should be skipped, not moved")
	}
}

// kind: any must match both a file and a directory with the same rule.
func TestKindAnyMatchesFileAndDir(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")

	// One file, one directory, both named to match the glob.
	fileSrc := filepath.Join(src, "report-a.txt")
	if err := os.WriteFile(fileSrc, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirSrc := filepath.Join(src, "report-b")
	if err := os.MkdirAll(dirSrc, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "Reports",
				Match:       config.Match{Globs: []string{"report-*"}, Kind: config.KindAny},
				Destination: dst,
				OnConflict:  config.ConflictRename,
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	if ok, _, err := e.HandleEntry(&cfg.Watches[0], fileSrc); err != nil || !ok {
		t.Fatalf("file should match kind:any rule: ok=%v err=%v", ok, err)
	}
	if ok, _, err := e.HandleEntry(&cfg.Watches[0], dirSrc); err != nil || !ok {
		t.Fatalf("dir should match kind:any rule: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "report-a.txt")); err != nil {
		t.Errorf("file not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "report-b")); err != nil {
		t.Errorf("dir not moved: %v", err)
	}
}

// kind: file must NOT match a dir even when the glob would.
func TestKindFileIgnoresDir(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	dirSrc := filepath.Join(src, "foo")
	if err := os.MkdirAll(dirSrc, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "FilesOnly",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	ok, _, err := e.HandleEntry(&cfg.Watches[0], dirSrc)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("kind:file rule should NOT match a directory")
	}
	if _, err := os.Stat(dirSrc); err != nil {
		t.Errorf("dir should still be in place: %v", err)
	}
}

// copyFile correctness: contents preserved, source unchanged, mode propagated.
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out", "dst.txt")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("contents differ: got %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source should remain: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: got %o want 0600", info.Mode().Perm())
	}
}

// copyFile must refuse to clobber an existing destination (O_EXCL guards this).
func TestCopyFileRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	dst := filepath.Join(dir, "b")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err == nil {
		t.Error("copyFile should refuse to overwrite an existing destination")
	}
}

// copyDir recursive structure preservation: nested dirs, file contents,
// symlinks recreated as symlinks (not followed).
func TestCopyDirNestedTreeAndSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("T"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "leaf.txt"), []byte("L"), 0o644); err != nil {
		t.Fatal(err)
	}
	// External target so we can confirm the symlink was recreated as a
	// symlink (and not followed and copied).
	external := filepath.Join(root, "external.txt")
	if err := os.WriteFile(external, []byte("E"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(src, "link")
	if err := os.Symlink(external, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	dst := filepath.Join(root, "dst")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "top.txt")); string(got) != "T" {
		t.Errorf("top.txt content lost: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "sub", "leaf.txt")); string(got) != "L" {
		t.Errorf("nested leaf content lost: %q", got)
	}
	info, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatalf("link missing in dst: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link was not recreated as a symlink (mode=%v) - it was followed and copied", info.Mode())
	}
	target, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if target != external {
		t.Errorf("symlink target rewritten: got %q want %q", target, external)
	}
}

// copyDir must refuse a non-directory source.
func TestCopyDirRefusesFileSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := copyDir(src, filepath.Join(dir, "out"))
	if err == nil {
		t.Error("copyDir should refuse a non-directory source")
	}
}

// copyDir must refuse to silently copy device nodes / sockets / pipes. This
// uses a FIFO (named pipe) because t.TempDir() lets us mknod it without
// elevated privileges.
func TestCopyDirRefusesDeviceLikeEntries(t *testing.T) {
	src := t.TempDir()
	if err := mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo unsupported on this platform: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	err := copyDir(src, dst)
	if err == nil {
		t.Error("copyDir should refuse non-regular non-dir non-symlink entries")
	}
}

// -----------------------------------------------------------------------------
// min_age behavior - the Finder "untitled folder" rename safety net
// -----------------------------------------------------------------------------

// A freshly-created directory matching a kind:dir rule whose min_age has not
// elapsed must be DEFERRED, not moved. retryAt must reflect mtime + min_age.
func TestMinAgeDefersFreshDirectory(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	loose := filepath.Join(src, "untitled folder")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "Loose folders",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindDir},
				Destination: dst,
				OnConflict:  config.ConflictRename,
				MinAge:      "1h", // way longer than the test
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	applied, retryAt, err := e.HandleEntry(&cfg.Watches[0], loose)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Errorf("fresh dir under 1h min_age must not be moved")
	}
	if retryAt.IsZero() {
		t.Errorf("retryAt should be set for a deferred entry")
	}
	// Source must still be in place.
	if _, err := os.Stat(loose); err != nil {
		t.Errorf("source dir should still exist: %v", err)
	}
	// retryAt should be roughly now + 1h (1m of slop for test timing).
	if d := time.Until(retryAt); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("retryAt unexpected: %v (want ~1h from now)", d)
	}
}

// Past the min_age threshold (faked via os.Chtimes), the same dir must move.
func TestMinAgeAllowsAfterThreshold(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	loose := filepath.Join(src, "ready folder")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	// Back-date the mtime so the entry already satisfies min_age.
	past := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(loose, past, past); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "Loose folders",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindDir},
				Destination: dst,
				OnConflict:  config.ConflictRename,
				MinAge:      "5m",
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	applied, retryAt, err := e.HandleEntry(&cfg.Watches[0], loose)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Errorf("dir older than min_age should move; deferred to %v instead", retryAt)
	}
	if _, err := os.Stat(filepath.Join(dst, "ready folder")); err != nil {
		t.Errorf("dir should now be in destination: %v", err)
	}
}

// The default min_age for a kind:dir rule (no MinAge set) is 5 minutes, which
// means a fresh dir must defer.
func TestMinAgeDefaultForDirIsFiveMinutes(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	loose := filepath.Join(src, "fresh")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "Loose folders",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindDir},
				Destination: dst,
				OnConflict:  config.ConflictRename,
				// MinAge intentionally unset - exercise the default.
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	applied, retryAt, err := e.HandleEntry(&cfg.Watches[0], loose)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Errorf("kind:dir default 5m min_age should defer a fresh directory")
	}
	if retryAt.IsZero() {
		t.Errorf("retryAt should be set under the default min_age")
	}
}

// Watch-level min_age applies when a rule doesn't set its own. A fresh file
// under a watch with min_age:1m must defer even though the rule is kind:file
// (default 0).
func TestWatchMinAgeAppliesToFileRule(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	fp := filepath.Join(src, "fresh.pdf")
	if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path:   src,
			MinAge: "1m",
			Rules: []config.Rule{{
				Name:        "PDFs",
				Match:       config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
				// MinAge intentionally unset - inherits from watch.
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	applied, retryAt, err := e.HandleEntry(&cfg.Watches[0], fp)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Error("watch min_age 1m should defer a fresh file even though rule kind is file")
	}
	if retryAt.IsZero() {
		t.Error("retryAt should be set when min_age defers")
	}
}

// A rule-level min_age must beat the watch-level one. Watch says 10m, rule
// says 0s, file moves now.
func TestRuleMinAgeOverridesWatchMinAge(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	fp := filepath.Join(src, "now.pdf")
	if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path:   src,
			MinAge: "10m",
			Rules: []config.Rule{{
				Name:        "PDFs",
				Match:       config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
				MinAge:      "0s",
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	applied, _, err := e.HandleEntry(&cfg.Watches[0], fp)
	if err != nil || !applied {
		t.Fatalf("rule min_age 0s should override watch 10m; applied=%v err=%v", applied, err)
	}
}

// Watch-level min_age does NOT override the rule's explicit value (positive
// case): rule says 1ms, watch says 10m, file moves once it's a few ms old.
func TestRuleMinAgeShortBeatsWatchMinAgeLong(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	fp := filepath.Join(src, "x.pdf")
	if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Bump mtime back so the 1ms threshold has clearly elapsed.
	old := time.Now().Add(-time.Second)
	if err := os.Chtimes(fp, old, old); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path:   src,
			MinAge: "10m",
			Rules: []config.Rule{{
				Name:        "PDFs",
				Match:       config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
				MinAge:      "1ms",
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	applied, _, err := e.HandleEntry(&cfg.Watches[0], fp)
	if err != nil || !applied {
		t.Fatalf("rule min_age 1ms should win over watch 10m; applied=%v err=%v", applied, err)
	}
}

// The default min_age for a kind:file rule is 0: fresh files move immediately.
func TestMinAgeDefaultForFileIsZero(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	fp := filepath.Join(src, "a.pdf")
	if err := os.WriteFile(fp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "PDFs",
				Match:       config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	applied, _, err := e.HandleEntry(&cfg.Watches[0], fp)
	if err != nil || !applied {
		t.Fatalf("kind:file default min_age should be 0; got applied=%v err=%v", applied, err)
	}
}

// -----------------------------------------------------------------------------
// ignore_globs - browser partial downloads, OS metadata, lock files
// -----------------------------------------------------------------------------

// A path matching the global ignore list must not be moved even when a rule's
// glob would otherwise match it. This is the core protection for in-progress
// browser downloads.
func TestGlobalIgnoreSkipsMatching(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	partial := filepath.Join(src, "report.pdf.crdownload")
	if err := os.WriteFile(partial, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		IgnoreGlobs: []string{"*.crdownload"},
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "Anything",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	applied, _, err := e.HandleEntry(&cfg.Watches[0], partial)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Errorf("ignored path %s should not be moved", partial)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Errorf("source should still exist: %v", err)
	}
}

// Per-watch ignore_globs extend the global list rather than replace it.
func TestPerWatchIgnoreExtendsGlobal(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	a := filepath.Join(src, "doc.crdownload")
	b := filepath.Join(src, "doc.weird")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		IgnoreGlobs: []string{"*.crdownload"},
		Watches: []config.Watch{{
			Path:        src,
			IgnoreGlobs: []string{"*.weird"},
			Rules: []config.Rule{{
				Name:        "Anything",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	if applied, _, err := e.HandleEntry(&cfg.Watches[0], a); err != nil || applied {
		t.Errorf("crdownload should be ignored (global): applied=%v err=%v", applied, err)
	}
	if applied, _, err := e.HandleEntry(&cfg.Watches[0], b); err != nil || applied {
		t.Errorf("weird should be ignored (per-watch): applied=%v err=%v", applied, err)
	}
}

// Safari downloads create a directory like "filename.pdf.download/" that the
// browser actively writes into. Our catch-all dir rule must NOT match it,
// and ScanOnce must not descend into it.
func TestIgnoredDirectoryNotMatchedAndNotDescended(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	saf := filepath.Join(src, "report.pdf.download")
	if err := os.MkdirAll(saf, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant something inside that, were we to descend, a rule could match.
	if err := os.WriteFile(filepath.Join(saf, "tempbits.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		IgnoreGlobs: []string{"*.download"},
		Watches: []config.Watch{{
			Path:      src,
			Recursive: true, // would otherwise descend into the .download dir
			Rules: []config.Rule{
				{
					Name:        "Loose folders",
					Match:       config.Match{Globs: []string{"*"}, Kind: config.KindDir},
					Destination: filepath.Join(dst, "dirs"),
					OnConflict:  config.ConflictRename,
					MinAge:      "0s",
				},
				{
					Name:        "PDFs",
					Match:       config.Match{Globs: []string{"*.pdf"}, Kind: config.KindFile},
					Destination: filepath.Join(dst, "pdfs"),
					OnConflict:  config.ConflictRename,
				},
			},
		}},
	}
	e := New(cfg, newTestLogger())

	// Direct HandleEntry on the .download dir: must be ignored.
	if applied, _, err := e.HandleEntry(&cfg.Watches[0], saf); err != nil || applied {
		t.Errorf(".download dir must be ignored by dir rule: applied=%v err=%v", applied, err)
	}

	// ScanOnce must skip descending - tempbits.pdf inside must not be moved.
	_, _, _, err := e.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "pdfs", "tempbits.pdf")); err == nil {
		t.Errorf("ScanOnce descended into ignored .download dir and moved tempbits.pdf")
	}
	if _, err := os.Stat(saf); err != nil {
		t.Errorf("Safari download dir should still exist: %v", err)
	}
}

// Sanity: ignore matching is case-insensitive (matches our glob semantics).
func TestIgnoreIsCaseInsensitive(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	partial := filepath.Join(src, "doc.CRDOWNLOAD")
	if err := os.WriteFile(partial, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		IgnoreGlobs: []string{"*.crdownload"},
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "Anything",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindFile},
				Destination: dst,
				OnConflict:  config.ConflictRename,
			}},
		}},
	}
	e := New(cfg, newTestLogger())
	if applied, _, err := e.HandleEntry(&cfg.Watches[0], partial); err != nil || applied {
		t.Errorf("case-insensitive ignore should skip %s: applied=%v err=%v", partial, applied, err)
	}
}

// ScanOnce must surface deferred entries in its return value rather than
// moving them. This is what `autoshelf once` logs and what the watcher
// re-queues from the initial sweep.
func TestScanOnceReportsDeferred(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "stash")
	loose := filepath.Join(src, "fresh")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Watches: []config.Watch{{
			Path: src,
			Rules: []config.Rule{{
				Name:        "Loose folders",
				Match:       config.Match{Globs: []string{"*"}, Kind: config.KindDir},
				Destination: dst,
				OnConflict:  config.ConflictRename,
				MinAge:      "1h",
			}},
		}},
	}
	e := New(cfg, newTestLogger())

	matched, scanned, deferred, err := e.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if matched != 0 {
		t.Errorf("nothing should have been moved, got matched=%d", matched)
	}
	if scanned == 0 {
		t.Errorf("at least the fresh dir should have been scanned")
	}
	if len(deferred) != 1 {
		t.Fatalf("expected 1 deferred entry, got %d", len(deferred))
	}
	if deferred[0].Path != loose {
		t.Errorf("deferred path: got %q want %q", deferred[0].Path, loose)
	}
	if _, err := os.Stat(loose); err != nil {
		t.Errorf("source must still be in place when deferred: %v", err)
	}
}

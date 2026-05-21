package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSampleYAML drops a minimal valid config alongside any extra files the
// test wants in the same directory, returning the path of the YAML.
func writeSampleYAML(t *testing.T, dir string) string {
	t.Helper()
	yaml := `watches:
  - path: ` + dir + `
    rules:
      - name: PDFs
        match:
          globs: ["*.pdf"]
        destination: ` + dir + `/sorted
`
	path := filepath.Join(dir, "autoshelf.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Regression for the original bug. In discovery mode, viper used to also
// accept a file literally named "autoshelf" (no extension) and feed its bytes
// to the YAML parser. Our compiled binary in the project dir was such a file,
// producing "invalid trailing UTF-8 octet". loadFromPaths must pick
// autoshelf.yaml and ignore the binary-shaped sibling.
func TestLoadDiscoveryIgnoresExtensionlessSibling(t *testing.T) {
	dir := t.TempDir()
	// Binary-shaped sibling - non-UTF8 bytes that would crash yaml.v3.
	if err := os.WriteFile(filepath.Join(dir, "autoshelf"), []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	yamlPath := writeSampleYAML(t, dir)

	cfg, err := loadFromPaths("", []string{dir})
	if err != nil {
		t.Fatalf("discovery Load failed: %v", err)
	}
	if cfg.SourceFile() != yamlPath {
		t.Errorf("loaded wrong file: got %q want %q", cfg.SourceFile(), yamlPath)
	}
}

// Discovery should walk searchPaths in order: the first directory containing
// an autoshelf.yaml wins.
func TestLoadDiscoveryHonorsSearchOrder(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	wantPath := writeSampleYAML(t, first)
	_ = writeSampleYAML(t, second)

	cfg, err := loadFromPaths("", []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceFile() != wantPath {
		t.Errorf("expected first dir's file %q, got %q", wantPath, cfg.SourceFile())
	}
}

func TestLoadExplicitPath(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writeSampleYAML(t, dir)
	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceFile() != yamlPath {
		t.Errorf("SourceFile mismatch: got %q want %q", cfg.SourceFile(), yamlPath)
	}
	if len(cfg.Watches) != 1 {
		t.Errorf("expected 1 watch, got %d", len(cfg.Watches))
	}
}

func TestValidateCatchesNoWatches(t *testing.T) {
	c := &Config{}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "no watches") {
		t.Errorf("expected 'no watches' error, got %v", err)
	}
}

func TestValidateCatchesEmptyWatchPath(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path: "",
		Rules: []Rule{{
			Name: "r", Destination: "/tmp/y",
			OnConflict: ConflictRename,
			Match:      Match{Globs: []string{"*.pdf"}, Kind: KindFile},
		}},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for empty watch path")
	}
}

func TestValidateCatchesWatchWithNoRules(t *testing.T) {
	c := &Config{Watches: []Watch{{Path: "/tmp/x", Rules: nil}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for watch with no rules")
	}
}

func TestValidateCatchesMissingDestination(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path:  "/tmp/x",
		Rules: []Rule{{Name: "r", Match: Match{Globs: []string{"*.pdf"}}}},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for missing destination")
	}
}

func TestValidateCatchesEmptyGlobs(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path:  "/tmp/x",
		Rules: []Rule{{Name: "r", Destination: "/tmp/y", Match: Match{Globs: []string{}}}},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for empty globs")
	}
}

func TestValidateCatchesBadGlob(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path:  "/tmp/x",
		Rules: []Rule{{Name: "r", Destination: "/tmp/y", OnConflict: ConflictRename, Match: Match{Globs: []string{"[bad"}, Kind: KindFile}}},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for malformed glob")
	}
}

// Whitespace-only globs must be rejected (normalize trims them to empty,
// which Validate flags). This locks in the trim + reject chain.
func TestValidateCatchesWhitespaceOnlyGlob(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path: "/tmp/x",
		Rules: []Rule{{
			Name: "r", Destination: "y",
			OnConflict: ConflictRename,
			Match:      Match{Globs: []string{"   "}, Kind: KindFile},
		}},
	}}}
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for whitespace-only glob (should have been trimmed to empty)")
	}
}

func TestValidateCatchesUnknownOnConflict(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path: "/tmp/x",
		Rules: []Rule{{
			Name:        "r",
			Destination: "/tmp/y",
			OnConflict:  "merge",
			Match:       Match{Globs: []string{"*.pdf"}, Kind: KindFile},
		}},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for unknown on_conflict")
	}
}

func TestValidateCatchesInvalidMinAge(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path: "/tmp/x",
		Rules: []Rule{{
			Name:        "r",
			Destination: "/tmp/y",
			OnConflict:  ConflictRename,
			Match:       Match{Globs: []string{"*.pdf"}, Kind: KindFile},
			MinAge:      "five minutes please",
		}},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for unparseable min_age")
	}
}

func TestValidateCatchesBadGlobalIgnoreGlob(t *testing.T) {
	c := &Config{
		IgnoreGlobs: []string{"[bad"},
		Watches: []Watch{{
			Path:  "/tmp/x",
			Rules: []Rule{{Name: "r", Destination: "/tmp/y", OnConflict: ConflictRename, Match: Match{Globs: []string{"*.pdf"}, Kind: KindFile}}},
		}},
	}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for malformed global ignore_glob")
	}
}

func TestValidateCatchesBadWatchIgnoreGlob(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path:        "/tmp/x",
		IgnoreGlobs: []string{"[bad"},
		Rules:       []Rule{{Name: "r", Destination: "/tmp/y", OnConflict: ConflictRename, Match: Match{Globs: []string{"*.pdf"}, Kind: KindFile}}},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for malformed watch ignore_glob")
	}
}

func TestEffectiveIgnoreGlobsCombines(t *testing.T) {
	c := &Config{IgnoreGlobs: []string{"*.crdownload"}}
	w := &Watch{IgnoreGlobs: []string{"*.tmp"}}
	got := c.EffectiveIgnoreGlobs(w)
	if len(got) != 2 || got[0] != "*.crdownload" || got[1] != "*.tmp" {
		t.Errorf("EffectiveIgnoreGlobs: got %v want [*.crdownload *.tmp]", got)
	}
}

func TestEffectiveIgnoreGlobsEmptyReturnsNil(t *testing.T) {
	c := &Config{}
	w := &Watch{}
	if got := c.EffectiveIgnoreGlobs(w); got != nil {
		t.Errorf("expected nil for empty ignores, got %v", got)
	}
}

func TestMinAgeDurationDefaults(t *testing.T) {
	dirRule := Rule{Match: Match{Kind: KindDir}}
	if got := dirRule.MinAgeDuration(); got != DefaultDirMinAge {
		t.Errorf("dir default: got %v want %v", got, DefaultDirMinAge)
	}
	fileRule := Rule{Match: Match{Kind: KindFile}}
	if got := fileRule.MinAgeDuration(); got != 0 {
		t.Errorf("file default: got %v want 0", got)
	}
	anyRule := Rule{Match: Match{Kind: KindAny}}
	if got := anyRule.MinAgeDuration(); got != 0 {
		t.Errorf("any default: got %v want 0", got)
	}
	explicit := Rule{Match: Match{Kind: KindDir}, MinAge: "30s"}
	if got := explicit.MinAgeDuration(); got != 30*time.Second {
		t.Errorf("explicit override: got %v want 30s", got)
	}
}

// EffectiveMinAge implements a precedence chain: rule > watch > kind-default.
// Each row in this table corresponds to one rung of that chain.
func TestEffectiveMinAgePrecedence(t *testing.T) {
	dirMatch := Match{Kind: KindDir}
	fileMatch := Match{Kind: KindFile}

	cases := []struct {
		name      string
		watchAge  string
		ruleAge   string
		ruleMatch Match
		want      time.Duration
	}{
		// Both empty: kind decides.
		{"dir kind default", "", "", dirMatch, DefaultDirMinAge},
		{"file kind default", "", "", fileMatch, 0},
		// Watch-only: applies regardless of kind, including overriding the
		// file default (which is 0).
		{"watch min_age applies to dir rule", "1m", "", dirMatch, time.Minute},
		{"watch min_age applies to file rule", "1m", "", fileMatch, time.Minute},
		// Rule-only: behaves as before.
		{"rule min_age beats kind default", "", "30s", dirMatch, 30 * time.Second},
		// Rule beats watch.
		{"rule overrides watch", "5m", "30s", dirMatch, 30 * time.Second},
		// Rule can lower below watch.
		{"rule lowers watch", "10m", "1s", fileMatch, time.Second},
		// Explicit "0s" on the rule disables - this is the documented
		// escape hatch from the dir default AND from any inherited watch
		// min_age. The contract is "the literal value the user wrote wins."
		{"rule 0s disables watch min_age", "10m", "0s", dirMatch, 0},
		{"rule 0s disables dir default", "", "0s", dirMatch, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := Watch{MinAge: tc.watchAge}
			r := Rule{MinAge: tc.ruleAge, Match: tc.ruleMatch}
			if got := w.EffectiveMinAge(r); got != tc.want {
				t.Errorf("EffectiveMinAge: got %v want %v (watch=%q rule=%q kind=%q)",
					got, tc.want, tc.watchAge, tc.ruleAge, tc.ruleMatch.Kind)
			}
		})
	}
}

// Empty min_age at every level falls through to the kind default. Sanity
// check for the all-zero-config path users get on a fresh autoshelf init.
func TestEffectiveMinAgeFallsThroughOnEmpty(t *testing.T) {
	w := Watch{}
	if got := w.EffectiveMinAge(Rule{Match: Match{Kind: KindDir}}); got != DefaultDirMinAge {
		t.Errorf("dir fallback: got %v want %v", got, DefaultDirMinAge)
	}
	if got := w.EffectiveMinAge(Rule{Match: Match{Kind: KindAny}}); got != 0 {
		t.Errorf("any fallback: got %v want 0", got)
	}
}

// Validate must reject a watch-level min_age that doesn't parse, the same
// way it rejects a rule-level one.
func TestValidateRejectsInvalidWatchMinAge(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path:   "/tmp/x",
		MinAge: "garbage",
		Rules: []Rule{{
			Name:        "r",
			Destination: "/tmp/y",
			OnConflict:  ConflictRename,
			Match:       Match{Globs: []string{"*.pdf"}, Kind: KindFile},
		}},
	}}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid watch min_age")
	}
	if !strings.Contains(err.Error(), "min_age") {
		t.Errorf("error should mention min_age, got %v", err)
	}
}

// Round-trip: a YAML file with watch-level min_age loads, normalizes
// (whitespace trimmed), validates, and round-trips through EffectiveMinAge.
func TestLoadAcceptsWatchLevelMinAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "autoshelf.yaml")
	yaml := `watches:
  - path: ` + dir + `
    min_age: "  2m  "
    rules:
      - name: PDFs
        match: { globs: ["*.pdf"] }
        destination: ` + dir + `/sorted
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Watches[0].MinAge; got != "2m" {
		t.Errorf("watch MinAge after normalize: got %q want %q", got, "2m")
	}
	got := cfg.Watches[0].EffectiveMinAge(cfg.Watches[0].Rules[0])
	if got != 2*time.Minute {
		t.Errorf("EffectiveMinAge from loaded yaml: got %v want 2m", got)
	}
}

func TestValidateCatchesUnknownKind(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path: "/tmp/x",
		Rules: []Rule{{
			Name:        "r",
			Destination: "/tmp/y",
			OnConflict:  ConflictRename,
			Match:       Match{Globs: []string{"*.pdf"}, Kind: "thing"},
		}},
	}}}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for unknown kind")
	}
}

func TestNormalizeAppliesDefaults(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path: "/tmp/x",
		Rules: []Rule{{
			Name:        "r",
			Destination: "y",
			Match:       Match{Globs: []string{"*.pdf"}},
		}},
	}}}
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	r := c.Watches[0].Rules[0]
	if r.OnConflict != ConflictRename {
		t.Errorf("OnConflict default: got %q want %q", r.OnConflict, ConflictRename)
	}
	if r.Match.Kind != KindFile {
		t.Errorf("Kind default: got %q want %q", r.Match.Kind, KindFile)
	}
	if r.Destination != filepath.Clean("/tmp/x/y") {
		t.Errorf("relative dest not resolved: got %q", r.Destination)
	}
}

// Defaults applied to case-shifted user input ("Rename" -> "rename" etc).
func TestNormalizeLowercasesCaseInsensitiveFields(t *testing.T) {
	c := &Config{Watches: []Watch{{
		Path: "/tmp/x",
		Rules: []Rule{{
			Name:        "  spaced  ",
			Destination: "y",
			OnConflict:  "  Rename  ",
			Match:       Match{Globs: []string{"  *.pdf  "}, Kind: "  DIR  "},
		}},
	}}}
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	r := c.Watches[0].Rules[0]
	if r.Name != "spaced" {
		t.Errorf("Name not trimmed: %q", r.Name)
	}
	if r.OnConflict != ConflictRename {
		t.Errorf("OnConflict not lowercased: %q", r.OnConflict)
	}
	if r.Match.Kind != KindDir {
		t.Errorf("Kind not lowercased: %q", r.Match.Kind)
	}
	if r.Match.Globs[0] != "*.pdf" {
		t.Errorf("Glob not trimmed: %q", r.Match.Globs[0])
	}
}

func TestExpandPathTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home: %v", err)
	}
	got, err := expandPath("~/Downloads/file.pdf")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Downloads", "file.pdf")
	if got != want {
		t.Errorf("~ expansion: got %q want %q", got, want)
	}
}

func TestExpandPathEnvVar(t *testing.T) {
	t.Setenv("AUTOSHELF_TESTVAR", "/some/dir")
	got, err := expandPath("$AUTOSHELF_TESTVAR/sub")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/some/dir/sub" {
		t.Errorf("$VAR expansion: got %q want %q", got, "/some/dir/sub")
	}
}

func TestExpandPathEmpty(t *testing.T) {
	got, err := expandPath("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("empty input should pass through, got %q", got)
	}
}

// AppliesToFile / AppliesToDir branch table - locks in every Kind case
// including the documented default-when-empty behavior.
func TestMatchAppliesTo(t *testing.T) {
	cases := []struct {
		kind     string
		wantFile bool
		wantDir  bool
	}{
		{"", true, false},
		{KindFile, true, false},
		{KindDir, false, true},
		{KindAny, true, true},
	}
	for _, tc := range cases {
		m := Match{Kind: tc.kind}
		if m.AppliesToFile() != tc.wantFile {
			t.Errorf("kind=%q AppliesToFile: got %v want %v", tc.kind, m.AppliesToFile(), tc.wantFile)
		}
		if m.AppliesToDir() != tc.wantDir {
			t.Errorf("kind=%q AppliesToDir: got %v want %v", tc.kind, m.AppliesToDir(), tc.wantDir)
		}
	}
}

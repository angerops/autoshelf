package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// DefaultDirMinAge is the min_age applied to kind:dir rules when none is set
// in the config. Tuned to outlast the typical Finder "untitled folder" rename
// window so we don't move a directory the user is mid-naming.
const DefaultDirMinAge = 5 * time.Minute

// Config is the root configuration loaded from YAML.
type Config struct {
	// DryRun, when true, logs intended moves without performing them.
	DryRun bool `mapstructure:"dry_run"`

	// IgnoreGlobs are case-insensitive shell globs matched against an entry's
	// base name; any matching entry is skipped before rules are evaluated.
	// Useful for browser partial-download artifacts and OS metadata files.
	// Patterns here apply to every watch; per-watch IgnoreGlobs extend the
	// list rather than replacing it.
	IgnoreGlobs []string `mapstructure:"ignore_globs"`

	// Watches is the list of folders to watch and the rules that apply to each.
	Watches []Watch `mapstructure:"watches"`

	// sourceFile records the path the config was loaded from, for logging.
	sourceFile string
}

// SourceFile returns the path of the YAML file Load resolved.
func (c *Config) SourceFile() string { return c.sourceFile }

// Watch is a single folder + the rules applied to files that land in it.
type Watch struct {
	// Path is the folder to watch. Tilde and environment variables are expanded.
	Path string `mapstructure:"path"`

	// Recursive controls whether subdirectories of Path are also watched.
	Recursive bool `mapstructure:"recursive"`

	// IgnoreGlobs extends the top-level Config.IgnoreGlobs with patterns
	// that apply only to this watch. Useful when one folder has a unique
	// temp-file convention.
	IgnoreGlobs []string `mapstructure:"ignore_globs"`

	// MinAge is the watch-level minimum age applied to every rule in this
	// watch that does not set its own min_age. Accepts a Go duration string
	// ("30s", "5m", "1h"). Lets you say "wait at least N before moving
	// anything out of ~/Downloads" once, instead of repeating min_age on
	// every rule. Precedence is rule > watch > kind-default (5m for
	// kind:dir, 0 for kind:file and kind:any).
	MinAge string `mapstructure:"min_age"`

	// Rules are evaluated in order. The first rule that matches a file wins.
	Rules []Rule `mapstructure:"rules"`
}

// EffectiveMinAge returns the min_age that should apply when r fires inside
// this watch. Precedence: the rule's own min_age (if set) wins; otherwise
// the watch's min_age (if set) wins; otherwise the kind-appropriate default
// (5m for kind:dir, 0 for kind:file / kind:any).
//
// Both r.MinAge and w.MinAge have been validated as parseable by the time
// this is called (normalize + Validate run during Load), so parse errors
// fall through to the same default the empty-string case uses.
func (w Watch) EffectiveMinAge(r Rule) time.Duration {
	if r.MinAge != "" {
		// Rule-level value wins, including an explicit "0s" that disables
		// the dir default. Honoring the literal value is the contract.
		if d, err := time.ParseDuration(r.MinAge); err == nil {
			return d
		}
	}
	if w.MinAge != "" {
		if d, err := time.ParseDuration(w.MinAge); err == nil {
			return d
		}
	}
	if r.Match.Kind == KindDir {
		return DefaultDirMinAge
	}
	return 0
}

// EffectiveIgnoreGlobs returns the concatenation of the global ignore list
// and this watch's ignore list. Order doesn't matter for matching - any hit
// wins.
func (c *Config) EffectiveIgnoreGlobs(w *Watch) []string {
	if len(c.IgnoreGlobs) == 0 && len(w.IgnoreGlobs) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.IgnoreGlobs)+len(w.IgnoreGlobs))
	out = append(out, c.IgnoreGlobs...)
	out = append(out, w.IgnoreGlobs...)
	return out
}

// Rule is a single match-and-act unit.
type Rule struct {
	// Name is a human-readable identifier used in logs.
	Name string `mapstructure:"name"`

	// Match holds the patterns that decide whether this rule applies.
	Match Match `mapstructure:"match"`

	// Destination is the folder files are moved into when the rule matches.
	// If the folder does not exist, it is created.
	Destination string `mapstructure:"destination"`

	// OnConflict controls what happens when a destination file or directory
	// of the same name already exists. One of: "rename" (default - suffix
	// with " (1)", " (2)" until free), "skip" (leave source untouched),
	// "error" (return an error and stop), "overwrite" (replace the
	// destination - dangerous, opt-in only).
	OnConflict string `mapstructure:"on_conflict"`

	// MinAge is the minimum time since the entry's last modification before
	// this rule will move it. Accepts a Go duration string like "5m", "30s",
	// "1h". When empty, defaults are applied per match kind: 5m for kind:dir,
	// 0 (no delay) for kind:file and kind:any. The check is against the
	// entry's mtime, so a directory the user is still naming or filling will
	// have its move pushed back automatically as content updates.
	MinAge string `mapstructure:"min_age"`
}

// MinAgeDuration returns the effective minimum age before this rule will act
// on an entry. After normalize() has validated MinAge, the parse is
// guaranteed to succeed, so any error is silently absorbed and the
// kind-appropriate default is returned.
func (r Rule) MinAgeDuration() time.Duration {
	if r.MinAge == "" {
		if r.Match.Kind == KindDir {
			return DefaultDirMinAge
		}
		return 0
	}
	d, err := time.ParseDuration(r.MinAge)
	if err != nil {
		if r.Match.Kind == KindDir {
			return DefaultDirMinAge
		}
		return 0
	}
	return d
}

// Conflict policy values.
const (
	ConflictRename    = "rename"
	ConflictSkip      = "skip"
	ConflictError     = "error"
	ConflictOverwrite = "overwrite"
)

// Match describes how an entry is selected for a rule.
type Match struct {
	// Globs are shell-style patterns (e.g. "*.pdf", "IMG_*.heic", "*").
	// Patterns are matched against the entry's base name, case-insensitively.
	Globs []string `mapstructure:"globs"`

	// Kind restricts what type of filesystem entry the rule applies to.
	// One of: "file" (default), "dir", "any". This is what lets you write a
	// catch-all rule like {globs: ["*"], kind: dir} to sweep stray folders
	// out of Downloads without also matching every file.
	Kind string `mapstructure:"kind"`
}

// Match kind values.
const (
	KindFile = "file"
	KindDir  = "dir"
	KindAny  = "any"
)

// AppliesToDir reports whether this match's Kind applies to directories.
func (m Match) AppliesToDir() bool {
	switch m.Kind {
	case KindDir, KindAny:
		return true
	default:
		return false
	}
}

// AppliesToFile reports whether the rule applies to regular files.
func (m Match) AppliesToFile() bool {
	switch m.Kind {
	case "", KindFile, KindAny:
		return true
	default:
		return false
	}
}

// defaultSearchPaths is where Load looks for autoshelf.yaml when no explicit
// path is provided. Exposed as a package var so tests can override it.
var defaultSearchPaths = []string{
	".",
	"$HOME/.config/autoshelf",
	"/etc/autoshelf",
}

// Load reads the config file at path (or discovers one on the default search
// paths if path is empty) and returns a validated Config.
func Load(path string) (*Config, error) {
	return loadFromPaths(path, defaultSearchPaths)
}

// loadFromPaths is the testable form of Load: pass an explicit search path
// list to avoid touching the real home or /etc.
func loadFromPaths(path string, searchPaths []string) (*Config, error) {
	v := viper.New()

	if path != "" {
		// Explicit file. The extension drives the parser; SetConfigType is only
		// needed if the file lacks a recognized extension.
		v.SetConfigFile(path)
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")); ext == "" {
			v.SetConfigType("yaml")
		}
	} else {
		// Discovery mode. Do NOT call SetConfigType here: it would make viper
		// also accept a file literally named "autoshelf" with no extension,
		// which on the dev box matches the compiled binary in the project
		// directory and gets fed to the YAML parser ("invalid trailing UTF-8
		// octet"). With SetConfigType unset, viper only considers files whose
		// extension matches a known parser (autoshelf.yaml, .yml, ...).
		v.SetConfigName("autoshelf")
		for _, p := range searchPaths {
			v.AddConfigPath(p)
		}
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, err
	}
	cfg.sourceFile = v.ConfigFileUsed()

	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalize expands paths and trims rule fields. Mutates cfg in place.
func (c *Config) normalize() error {
	for i, g := range c.IgnoreGlobs {
		c.IgnoreGlobs[i] = strings.TrimSpace(g)
	}

	for i := range c.Watches {
		w := &c.Watches[i]
		expanded, err := expandPath(w.Path)
		if err != nil {
			return err
		}
		w.Path = expanded

		for k, g := range w.IgnoreGlobs {
			w.IgnoreGlobs[k] = strings.TrimSpace(g)
		}

		w.MinAge = strings.TrimSpace(w.MinAge)

		for j := range w.Rules {
			r := &w.Rules[j]
			r.Name = strings.TrimSpace(r.Name)
			r.OnConflict = strings.ToLower(strings.TrimSpace(r.OnConflict))
			if r.OnConflict == "" {
				r.OnConflict = ConflictRename
			}
			r.Match.Kind = strings.ToLower(strings.TrimSpace(r.Match.Kind))
			if r.Match.Kind == "" {
				r.Match.Kind = KindFile
			}
			r.MinAge = strings.TrimSpace(r.MinAge)

			if r.Destination != "" {
				dest, err := expandPath(r.Destination)
				if err != nil {
					return err
				}
				// Resolve relative destinations against the watched folder.
				if !filepath.IsAbs(dest) {
					dest = filepath.Join(w.Path, dest)
				}
				r.Destination = dest
			}

			for k, g := range r.Match.Globs {
				r.Match.Globs[k] = strings.TrimSpace(g)
			}
		}
	}
	return nil
}

// Validate returns the first structural problem in cfg, or nil if it looks OK.
func (c *Config) Validate() error {
	for k, g := range c.IgnoreGlobs {
		if g == "" {
			return fmt.Errorf("ignore_globs[%d]: empty pattern", k)
		}
		if _, err := filepath.Match(g, "probe"); err != nil {
			return fmt.Errorf("ignore_globs[%d] (%q): %w", k, g, err)
		}
	}
	if len(c.Watches) == 0 {
		return fmt.Errorf("config has no watches defined")
	}
	for i, w := range c.Watches {
		if w.Path == "" {
			return fmt.Errorf("watches[%d]: path is required", i)
		}
		for k, g := range w.IgnoreGlobs {
			if g == "" {
				return fmt.Errorf("watches[%d].ignore_globs[%d]: empty pattern", i, k)
			}
			if _, err := filepath.Match(g, "probe"); err != nil {
				return fmt.Errorf("watches[%d].ignore_globs[%d] (%q): %w", i, k, g, err)
			}
		}
		if w.MinAge != "" {
			if _, err := time.ParseDuration(w.MinAge); err != nil {
				return fmt.Errorf("watches[%d] (%s): invalid min_age %q: %w", i, w.Path, w.MinAge, err)
			}
		}
		if len(w.Rules) == 0 {
			return fmt.Errorf("watches[%d] (%s): at least one rule is required", i, w.Path)
		}
		for j, r := range w.Rules {
			if r.Destination == "" {
				return fmt.Errorf("watches[%d].rules[%d] (%s): destination is required", i, j, r.Name)
			}
			switch r.OnConflict {
			case ConflictRename, ConflictSkip, ConflictError, ConflictOverwrite:
			default:
				return fmt.Errorf("watches[%d].rules[%d] (%s): on_conflict must be one of rename|skip|error|overwrite, got %q", i, j, r.Name, r.OnConflict)
			}
			switch r.Match.Kind {
			case KindFile, KindDir, KindAny:
			default:
				return fmt.Errorf("watches[%d].rules[%d] (%s): match.kind must be one of file|dir|any, got %q", i, j, r.Name, r.Match.Kind)
			}
			if r.MinAge != "" {
				if _, err := time.ParseDuration(r.MinAge); err != nil {
					return fmt.Errorf("watches[%d].rules[%d] (%s): invalid min_age %q: %w", i, j, r.Name, r.MinAge, err)
				}
			}
			if len(r.Match.Globs) == 0 {
				return fmt.Errorf("watches[%d].rules[%d] (%s): match.globs must have at least one pattern", i, j, r.Name)
			}
			for k, g := range r.Match.Globs {
				if g == "" {
					return fmt.Errorf("watches[%d].rules[%d].match.globs[%d]: empty pattern", i, j, k)
				}
				// filepath.Match validates the pattern syntax even if no real filename is supplied.
				if _, err := filepath.Match(g, "probe"); err != nil {
					return fmt.Errorf("watches[%d].rules[%d].match.globs[%d] (%q): %w", i, j, k, g, err)
				}
			}
		}
	}
	return nil
}

// expandPath resolves ~ and environment variables to an absolute-ish path.
func expandPath(p string) (string, error) {
	if p == "" {
		return p, nil
	}
	p = os.ExpandEnv(p)
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Clean(p), nil
}

package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// sampleConfig is what `autoshelf init` writes to disk.
// Keep this synchronized with autoshelf.example.yaml at the repo root - both
// should be character-identical so the file in the repo is exactly what users
// receive from `autoshelf init`. (Go's go:embed cannot reach files outside
// the cmd/ subtree, hence the duplication.)
const sampleConfig = `# =============================================================================
# autoshelf configuration
# =============================================================================
#
# Quick reference
# ---------------
# ignore_globs:             case-insensitive globs that skip an entry before
#                           any rule sees it (browser temp files, OS metadata).
#                           See below for the recommended default list.
# watches:                  list of folders to monitor
#   path:                   folder to watch ("~", "$HOME", env vars expanded)
#   recursive:              true to also watch subfolders (default: false)
#   ignore_globs:           per-watch ignore list, EXTENDS the global list
#                           rather than replacing it
#   min_age:                watch-level default min_age applied to every
#                           rule in this watch that does not set its own.
#                           Go duration string ("30s", "5m", "1h").
#                           Precedence: rule min_age > watch min_age >
#                           kind default (5m for dir, 0 for file/any).
#                           Useful for "wait at least N before moving
#                           anything out of ~/Downloads."
#   rules:                  ordered list - FIRST MATCHING RULE WINS, so put
#                           narrow rules above broad ones
#     name:                 label shown in logs (free text)
#     match:
#       globs:              one or more shell globs against the basename
#                           (case-insensitive, OR'd together)
#       kind:               which kind of filesystem entry to match
#                             file (default) - regular files only
#                             dir            - directories only
#                             any            - either
#     destination:          target folder
#                             - absolute path (~ and env vars expanded), OR
#                             - relative path (resolved against the watched
#                               folder's ` + "`path`" + ` above)
#                             - created automatically if it doesn't exist
#     on_conflict:          what to do when the destination name is taken
#                             rename (default) - suffix " (1)", " (2)", ...
#                                                until free. Never loses data.
#                             skip             - leave source in place, log,
#                                                move on. Never loses data.
#                             error            - fail the move and surface
#                                                an error in the logs.
#                             overwrite        - RemoveAll(dst), then move.
#                                                The only mode that can lose
#                                                data. Opt in deliberately.
#     min_age:              minimum time since the entry was last modified
#                           before this rule will act on it. Go duration
#                           string ("5m", "30s", "1h"). Defaults:
#                             kind:dir  -> 5m  (Finder rename safety)
#                             kind:file -> 0   (move immediately after debounce)
#                             kind:any  -> 0
#                           Set "0s" explicitly to disable the dir default.
#                           If the entry is touched again (e.g. you drop more
#                           files into the dir), its mtime updates and the
#                           min_age window restarts - so "5 minutes since the
#                           last change" is the effective semantic.
#
# Safety
# ------
# - Every configured ` + "`destination`" + ` across every rule is added to a protected
#   set. Those exact paths are never matched by ANY rule - so a catch-all
#   rule like {globs: ["*"], kind: dir} cannot move Installers/, Archives/,
#   or its own destination. You can use catch-alls without fear.
# - Cross-filesystem moves (~/Downloads to an external drive, for example)
#   use copy + remove for both files and whole directory trees. A partial
#   cross-device copy is rolled back so you never end up with half a tree
#   at the destination and the original gone.
# - Symlinks are skipped. Devices, sockets and pipes are refused (never
#   silently copied or deleted).
# - Directories under the default 5m min_age won't be moved while you're
#   mid-naming them in Finder. The watcher remembers them and re-checks
#   automatically once the threshold has passed.
#
# Tip: set dry_run: true or pass --dry-run while iterating on rules. You'll
# see every move the engine WOULD have made without anything actually moving,
# and you'll also see "deferred" entries for anything held back by min_age.
# =============================================================================

# Log intended moves without performing them. Override per-invocation with
# --dry-run. Leave false for normal operation.
dry_run: false

# Names (case-insensitive globs against the base name) that should be skipped
# entirely - they are never matched by any rule, and ignored directories are
# not descended into during scans. The defaults below cover the common
# browser partial-download patterns and macOS metadata files, so a download
# in progress can't be moved out from under the browser, and a Safari
# ".download" directory (yes, it's a directory) can't be eaten by a
# catch-all kind:dir rule.
ignore_globs:
  - "*.crdownload"        # Chrome, Edge, Brave, Opera
  - "*.part"              # Firefox, others
  - "*.partial"           # older Edge, some torrent clients
  - "*.download"          # Safari (this one is a DIRECTORY)
  - "*.tmp"               # generic temp marker
  - "*.swp"               # vim swap
  - "*.swo"               # vim swap, older
  - "~$*"                 # Microsoft Office lock files
  - ".~lock.*"            # LibreOffice lock files
  - ".DS_Store"           # macOS Finder metadata
  - ".localized"          # macOS i18n marker
  - ".Spotlight-V100"     # macOS Spotlight metadata
  - ".Trashes"            # macOS trash metadata

watches:

  # ===========================================================================
  # ~/Downloads - the main event
  # ===========================================================================
  - path: ~/Downloads
    recursive: false           # only the top level; don't dive into subfolders

    rules:

      # -----------------------------------------------------------------------
      # Screenshots
      # Multiple globs are OR'd together. macOS uses "Screen Shot", iOS share
      # sheet uses "Screenshot", CleanShot prefixes with "CleanShot".
      # kind, on_conflict and min_age are omitted, so the defaults
      # (file / rename / 0) apply.
      # -----------------------------------------------------------------------
      - name: Screenshots
        match:
          globs:
            - "Screen Shot *.png"
            - "Screenshot *.png"
            - "CleanShot *.png"
        destination: ~/Pictures/Screenshots

      # -----------------------------------------------------------------------
      # PDFs - the canonical simple rule: one extension, absolute destination.
      # -----------------------------------------------------------------------
      - name: PDFs
        match:
          globs: ["*.pdf"]
        destination: ~/Documents/PDFs

      # -----------------------------------------------------------------------
      # macOS installers - destination is RELATIVE, so this resolves to
      # ~/Downloads/Installers automatically. Handy when you want a tidy
      # subfolder right next to the original download location.
      # -----------------------------------------------------------------------
      - name: macOS installers
        match:
          globs: ["*.dmg", "*.pkg"]
        destination: Installers          # relative -> ~/Downloads/Installers

      # -----------------------------------------------------------------------
      # Archives with on_conflict: skip.
      # If you re-download the same archive (foo-1.2.3.zip), the default
      # "rename" mode would create foo-1.2.3 (1).zip. With "skip", the new
      # copy stays in ~/Downloads so you can decide manually whether to
      # replace the existing one.
      # -----------------------------------------------------------------------
      - name: Archives
        match:
          globs: ["*.zip", "*.tar.gz", "*.tgz", "*.7z", "*.rar"]
        destination: Archives
        on_conflict: skip

      # -----------------------------------------------------------------------
      # Images - broad extension list, kind/on_conflict/min_age written
      # explicitly to show the syntax.
      # -----------------------------------------------------------------------
      - name: Images
        match:
          globs: ["*.jpg", "*.jpeg", "*.png", "*.heic", "*.gif", "*.webp"]
          kind: file                     # default; written here for clarity
        destination: ~/Pictures/Inbox
        on_conflict: rename              # default; written here for clarity
        min_age: 0s                      # default for files; written here for clarity

      # -----------------------------------------------------------------------
      # Catch-all for stray subdirectories.
      # Pattern: globs ["*"] + kind: dir matches every directory at the top
      # level of ~/Downloads. The destinations above (Installers/, Archives/,
      # Misc Folders/ itself) are PROTECTED, so this rule cannot eat them
      # even though "*" would otherwise match. min_age defaults to 5m for
      # dir rules: this gives Finder time to finish renaming "untitled
      # folder" before we grab it. Drop to 1m if you trust yourself to name
      # folders fast; raise it if you sometimes leave dirs half-named on
      # purpose. Setting "0s" disables the protection (NOT recommended).
      # -----------------------------------------------------------------------
      - name: Loose folders
        match:
          globs: ["*"]
          kind: dir
        destination: Misc Folders
        on_conflict: rename
        min_age: 5m

  # ===========================================================================
  # ~/Desktop - lighter touch, just sweep screenshots that miss Downloads.
  # ===========================================================================
  - path: ~/Desktop
    recursive: false
    rules:
      - name: Stray screenshots
        match:
          globs: ["Screen Shot *.png", "Screenshot *.png"]
        destination: ~/Pictures/Screenshots

  # ===========================================================================
  # ~/Work/Incoming - example of recursive watching + on_conflict: error.
  #
  # Uncomment and adjust the path to enable. Demonstrates:
  #   - recursive: true     watch the whole subtree
  #   - kind: any           apply to files OR directories with the same glob
  #   - on_conflict: error  fail loudly rather than silently rename - use
  #                         this when a duplicate name means something has
  #                         gone wrong upstream and you want to know
  #   - min_age: 10m        wait until the upstream process has finished
  #                         writing the bundle before treating it as ready
  # ===========================================================================
  # - path: ~/Work/Incoming
  #   recursive: true
  #   rules:
  #     - name: Client reports
  #       match:
  #         globs: ["report-*.pdf", "report-*"]
  #         kind: any
  #       destination: ~/Work/Reports
  #       on_conflict: error
  #       min_age: 10m

  # ===========================================================================
  # ~/Pictures/Camera Roll - example of on_conflict: overwrite.
  #
  # Uncomment to enable. Use overwrite ONLY when you're sure the incoming
  # copy is the authoritative one - for example, when re-syncing the same
  # exported file. Overwrite is the only mode that can lose data.
  # ===========================================================================
  # - path: ~/Pictures/Camera Roll/Inbox
  #   recursive: false
  #   rules:
  #     - name: Latest exports win
  #       match:
  #         globs: ["IMG_*.jpg", "IMG_*.heic"]
  #       destination: ~/Pictures/Camera Roll/Sorted
  #       on_conflict: overwrite
`

var initOutput string

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write a sample config to disk",
	RunE: func(cmd *cobra.Command, args []string) error {
		target := initOutput
		if target == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			target = filepath.Join(home, ".config", "autoshelf", "autoshelf.yaml")
		}

		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("refusing to overwrite existing file: %s", target)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(sampleConfig), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote sample config: %s\n", target)
		return nil
	},
}

func init() {
	initCmd.Flags().StringVarP(&initOutput, "output", "o", "", "where to write the sample config (default: ~/.config/autoshelf/autoshelf.yaml)")
	rootCmd.AddCommand(initCmd)
}

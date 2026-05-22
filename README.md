# autoshelf

Your files, self-organized.

autoshelf watches folders you choose and moves files where they belong, based
on rules in a YAML config. Set a rule once. Never think about it again.

## How it works

You define a list of **watches** (folders to monitor) and one or more **rules**
per watch. Each rule has:

- A set of **glob patterns** that select files (`*.pdf`, `IMG_*.heic`, ...)
- A **destination** folder to move matched files into

Rules inside a watch are tried in order; the first match wins. Missing
destination directories are created automatically.

The watcher uses OS-level filesystem events (fsnotify) and runs an initial
sweep of each watched folder on startup so existing files get sorted too.

## Watching network shares

Real-time events only fire for syscalls on the local machine, so writes
that another host makes into a mounted SMB / NFS / AFP share won't be
picked up immediately. Run `autoshelf once` on a timer for those.
Details and recipes in [docs/network-shares.md](docs/network-shares.md).

## Install

### Homebrew (recommended on macOS / Linuxbrew)

```bash
brew install angerops/tap/autoshelf
brew services start autoshelf
```

Homebrew handles the binary install, generates the launchd plist (macOS) or
systemd user unit (Linux), and supervises the daemon for you. Logs land at
`$(brew --prefix)/var/log/autoshelf.log`; `autoshelf log -f` finds them
automatically.

### From source

Requires Go 1.22+.

```bash
make build         # produces ./autoshelf
make install       # installs to ~/.local/bin/autoshelf
```

Cross-builds and version-stamping are documented in
[CONTRIBUTING.md](CONTRIBUTING.md).

## Configure

Generate a starter config:

```bash
autoshelf init
# wrote sample config: ~/.config/autoshelf/autoshelf.yaml
```

Or copy `autoshelf.example.yaml` and edit. Config discovery order:

1. `--config` / `-c` flag
2. `./autoshelf.yaml`
3. `~/.config/autoshelf/autoshelf.yaml`
4. `/etc/autoshelf/autoshelf.yaml`

### Live config reload

`autoshelf run` watches the config file and reloads on save — no
restart, no `launchctl kickstart`. A sweep runs immediately after each
reload, so new rules apply to files already sitting in the watch
folder, not just new arrivals.

If the new YAML is malformed or fails validation, the daemon logs the
error and **keeps running on the previous good config** — you won't
lose service to a typo. Watch paths reconcile in place; the rule
engine rebuilds so changed destinations and `on_conflict` settings
apply immediately.

### Config schema

```yaml
dry_run: false              # log moves instead of performing them

ignore_globs:               # OPTIONAL global skip list (see below)
  - "*.crdownload"
  - "*.part"
  - "*.download"
  - ".DS_Store"

watches:
  - path: ~/Downloads       # folder to watch (env vars and ~ expanded)
    recursive: false        # also watch subfolders?
    ignore_globs: []        # optional per-watch additions
    min_age: 0s             # OPTIONAL watch-level default; rules
                            # without their own min_age inherit it.
    rules:
      - name: PDFs          # shows up in logs
        match:
          globs:            # case-insensitive shell globs against base name
            - "*.pdf"
          kind: file        # file (default) | dir | any
        destination: ~/Documents/PDFs   # absolute, or relative to `path`
        on_conflict: rename # rename (default) | skip | error | overwrite
        min_age: 0s         # Go duration. Default: 5m for kind:dir, 0 for others.
```

Notes:

- Destinations may be absolute, or relative to the watched folder's path.
- Glob matching is case-insensitive and applies to the entry's base name.
- Cross-filesystem moves (e.g. `~/Downloads` to an external drive) fall
  back to copy + remove, for files and entire directory trees alike.

#### Deferring fresh entries: `min_age`

`min_age` is the smallest time-since-last-modification an entry must
have before a rule will act on it. It exists for cases like the macOS
Finder rename window: Finder creates "untitled folder", autoshelf sees
the CREATE event, and would move it before you finish typing the real
name. Default for `kind: dir` is **5 minutes** (outlasts the Finder
window); default for `kind: file` and `kind: any` is **0** (move
immediately after the debounce window).

The check is against the entry's mtime, so any touch — renaming,
dropping files inside a directory — resets the clock. In practice:
"wait until the entry has been quiet for N." `autoshelf run` re-checks
deferred entries automatically at the new threshold without needing a
fresh filesystem event. `autoshelf once` logs deferred entries and
exits; they'll be picked up on the next invocation.

To override the default explicitly:

```yaml
- name: Loose folders
  match: { globs: ["*"], kind: dir }
  destination: Misc Folders
  min_age: 10m        # be even more patient
```

`min_age` can also be set on the **watch itself** as a default for every
rule under it that doesn't specify its own. Useful when you want a single
"settling time" for the whole folder rather than repeating it on each rule:

```yaml
watches:
  - path: ~/Downloads
    min_age: 1m              # nothing in Downloads is touched for 1 minute
    rules:
      - name: PDFs
        match: { globs: ["*.pdf"] }
        destination: ~/Documents/PDFs
        # No rule-level min_age, so 1m from the watch applies.
      - name: Fast lane
        match: { globs: ["*.scr"] }
        destination: ~/Downloads/Screenshots
        min_age: 0s          # opt out of the watch default - move immediately
```

Precedence is **rule `min_age` > watch `min_age` > kind default** (5m
for `kind:dir`, 0 for `kind:file` and `kind:any`). An explicit `0s` at
any level wins the same way an explicit duration does — that's how you
opt out of an inherited delay.

```yaml
- name: Slow downloads
  match: { globs: ["*.iso"], kind: file }
  destination: ISOs
  min_age: 30s        # extra confidence the download fully finished
```

Setting `min_age: 0s` on a catch-all dir rule disables the
Finder-rename protection — only do it if you really mean to.

#### Ignoring browser temp files and OS metadata: `ignore_globs`

Browsers leave breadcrumbs during downloads that can collide with
broad rules. Chrome/Edge/Brave/Opera write `*.crdownload`; Firefox
writes `*.part`; Safari is the trickiest — it creates a *directory*
named `filename.pdf.download/` and writes the bytes inside, so a
catch-all `globs: ["*"], kind: dir` rule would happily move an
in-progress Safari download out from under the browser.

`ignore_globs` is a list of case-insensitive shell globs matched against
each entry's base name. Anything matching is skipped before any rule sees
it; ignored directories are not descended into during scans.

```yaml
# Global - applies to every watch.
ignore_globs:
  - "*.crdownload"        # Chrome, Edge, Brave, Opera
  - "*.part"              # Firefox
  - "*.download"          # Safari (directory)
  - "*.tmp"
  - "~$*"                 # MS Office lock files
  - ".DS_Store"           # macOS Finder metadata

watches:
  - path: ~/Work/Incoming
    # Per-watch list EXTENDS the global one. Useful when one folder has a
    # unique convention.
    ignore_globs:
      - "*.uploading"
    rules:
      - name: ...
```

`autoshelf init` writes a sample config that already includes the
common browser and macOS patterns, so you get this protection out of
the box.

#### Handling collisions: `on_conflict`

Every rule has a collision policy. Pick the one that matches the risk you're
willing to take:

- `rename` (default) — append ` (1)`, ` (2)`, ... until the destination name
  is free. Nothing is ever overwritten; you may accumulate variants, but you
  never lose data.
- `skip` — leave the source file in place and do nothing. Useful when you'd
  rather notice the duplicate yourself before deciding.
- `error` — fail the move and surface an error in the logs. The source stays
  in place. Useful when a collision should never happen and you want a noisy
  signal if it does.
- `overwrite` — remove the destination (file or whole directory tree)
  and move the source into its place. Use only when you're certain
  the new copy supersedes the old one; this is the only mode that can
  lose data.

#### Sweeping stray directories: `match.kind: dir`

To clean up loose folders that pile up in `~/Downloads` without disturbing
the folders you actually use as destinations:

```yaml
- name: Loose folders
  match:
    globs: ["*"]
    kind: dir
  destination: Misc Folders
  on_conflict: rename
```

Every rule's `destination` is automatically protected: a catch-all dir
rule cannot move `Installers/`, `Archives/`, or its own
`Misc Folders/`, even though `*` would otherwise match them. This is
what makes catch-all rules safe to use.

`kind: any` exists for the rare case where the same rule should apply
to both files and directories. The default is `file`.

## Commands

```bash
autoshelf run                     # watch forever (Ctrl-C to stop)
autoshelf once                    # one-shot scan, exit
autoshelf validate                # parse config and report
autoshelf init -o ./autoshelf.yaml
autoshelf log                     # print the log file
autoshelf log -f                  # follow the log (tail -f)

# Global flags:
#   -c, --config <path>   override config location
#       --dry-run         force dry-run regardless of config
#   -v, --verbose         debug-level logging (includes ignore-skip records)
```

### Viewing the log

`autoshelf log` streams the log file written by the launchd / systemd unit.
Defaults:

- macOS: `~/Library/Logs/autoshelf.log`
- Linux: `$XDG_STATE_HOME/autoshelf/autoshelf.log`, falling back to
  `~/.local/state/autoshelf/autoshelf.log`

Override with `--file`. `-f` / `--follow` tails the file the way
`tail -f` does — Ctrl-C exits cleanly. Output is colorized when stdout
is a terminal and plain text when piped (`autoshelf log | grep moved`),
so it works equally well for browsing and for filtering.

## Running as a background service

`brew services start autoshelf` covers this automatically — brew
generates the launchd plist (macOS) or systemd user unit (Linux) and
supervises the daemon for you.

If you're managing the service manually (no Homebrew, or a controlled
environment where you write your own unit files), see
[docs/background-service.md](docs/background-service.md).

## Documentation

- [docs/background-service.md](docs/background-service.md) — manual
  launchd / systemd setup
- [docs/network-shares.md](docs/network-shares.md) — watching mounted
  SMB / NFS / AFP shares
- [docs/troubleshoot.md](docs/troubleshoot.md) — failure modes and
  fixes
- [CONTRIBUTING.md](CONTRIBUTING.md) — building, testing, releasing

## License

[0BSD](LICENSE). autoshelf is provided as-is with no warranty; in
particular, see the license's disclaimer of liability for data loss.
Test new rules with `--dry-run` first, especially anything using
`on_conflict: overwrite`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for building, running tests,
the repository layout, and the release process.

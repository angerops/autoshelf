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

## Limitations

### Network shares (SMB, NFS, AFP)

autoshelf gets real-time events from the kernel — kqueue on macOS, inotify
on Linux. Neither one delivers events for changes that originate on a
*different machine*. If a remote host drops a file into a share you have
mounted locally, your Mac's kernel sees a stale dirent, not a write event,
so the daemon's watch will not fire.

Two scenarios to keep separate:

- **Writes from this machine into a mounted share** — these are local
  syscalls. kqueue / inotify see them; autoshelf reacts in real time. Same
  for sync-client writes (Dropbox, iCloud Drive, Google Drive, OneDrive) —
  from the kernel's perspective the sync client is just a local process
  writing into a local directory.
- **Writes from another machine into the same share** — kernel sees no
  event. autoshelf will not pick up the file until the next time something
  brings the watch around to it.

The pragmatic workaround is `autoshelf once` on a timer. It runs the same
rule engine in one-shot mode (initial sweep, exit) and exits cleanly, so
it's safe to fire from `cron`, a `launchd` `StartInterval` plist, or a
systemd timer. Pick a cadence that matches how quickly you need files
sorted — every 5 minutes is plenty for most "drop a file on the NAS" flows.

If you want to mix modes (real-time for `~/Downloads`, polling for
`/Volumes/NAS/Inbox`), run `autoshelf run` for the local watches and a
separate `autoshelf once` on a timer with a config that only declares the
remote watches. Both invocations are independent processes; they don't
interfere.

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
make build                 # produces ./autoshelf for the current OS/arch
make build-darwin-arm64    # cross-build for Apple Silicon
make build-linux-amd64     # cross-build for x86_64 Linux
make install               # installs to ~/.local/bin/autoshelf
make VERSION=v0.1.0 build  # stamp a specific version into the binary
```

`make build` uses `git describe --tags --dirty` to populate the `--version`
output. Pass `VERSION=...` to override.

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

`autoshelf run` watches the resolved config file and reloads it whenever you
save changes — no restart, no `launchctl kickstart`. Add a rule, save the
file, and the next initial-style sweep runs immediately so files that the
new rule covers get sorted right away (this is also what catches files that
were sitting unmatched before you added the rule).

The reload pipeline is conservative:

- The new file is parsed and validated *before* anything is swapped. If
  the YAML is malformed or fails validation, the daemon logs the error
  and **keeps running on the previous good config**. You won't lose
  service to a typo.
- Watch paths are reconciled in place: any new `path:` gets registered
  with fsnotify; any removed `path:` is unwatched. The rule engine is
  rebuilt so protected destinations and `on_conflict` settings reflect
  the new file.
- The mechanism uses a filesystem watch on the config file's parent
  directory, so editor save styles that write to a temp file and rename
  (vim, VS Code, etc.) are handled. Bursts of events during a save are
  debounced so one reload happens per save.

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
    min_age: 0s             # OPTIONAL watch-level default min_age (Go duration).
                            # Applied to every rule under this watch that
                            # does not set its own min_age. See below.
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

- Destinations may be absolute or relative. A relative destination is joined
  onto the watched folder's path.
- Glob matching is case-insensitive and applies to the entry's base name.
- Cross-filesystem moves (e.g. `~/Downloads` to an external drive) are handled
  with a copy + remove fallback. This works for files and for whole directory
  trees.

#### Deferring fresh entries: `min_age`

`min_age` is the smallest "time since last modification" an entry must have
before a rule will act on it. It exists to handle the case where a tool (most
commonly macOS Finder) creates an entry under a default name, the watcher
sees the `CREATE` event, and would move the entry before you could finish
typing the real name. Default `min_age` for a `kind: dir` rule is **5
minutes**, which outlasts the Finder rename window comfortably; default for
`kind: file` and `kind: any` is **0** (move immediately after the debounce
window).

The check is against the entry's mtime, so any touch (renaming, dropping
files inside a directory) resets the clock — the effective semantic is "wait
until the entry has been quiet for N". When `autoshelf run` defers an entry
it remembers it and re-checks automatically at the new threshold, so you
don't need a new filesystem event to trigger re-evaluation. `autoshelf once`
logs deferred entries as INFO and exits; they'll be re-picked-up next time
something brings the watcher around to them.

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

Precedence is **rule `min_age` &gt; watch `min_age` &gt; kind default** (5m
for `kind:dir`, 0 for `kind:file` and `kind:any`). An explicit `0s` at any
level wins the same way an explicit duration does — that's how you opt out
of an inherited delay.

```yaml
- name: Slow downloads
  match: { globs: ["*.iso"], kind: file }
  destination: ISOs
  min_age: 30s        # extra confidence the download fully finished
```

Setting `min_age: 0s` explicitly disables the dir default (not recommended
for catch-all dir rules — that's the bug you're protecting yourself from).

#### Ignoring browser temp files and OS metadata: `ignore_globs`

Browsers leave breadcrumbs while a download is in flight, and those
breadcrumbs can collide with broad rules. Chrome/Edge/Brave/Opera write
`*.crdownload`. Firefox writes `*.part`. Safari is the trickiest one — it
creates a *directory* named `filename.pdf.download/` and writes the bytes
inside, which means a catch-all `globs: ["*"], kind: dir` rule would happily
move an in-progress Safari download out from under the browser.

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

`autoshelf init` writes a sample config that already includes the common
browser and macOS patterns, so you get the protection automatically. The
match is case-insensitive (so `*.crdownload` catches `Doc.CRDOWNLOAD` too)
and uses the same glob syntax as rule matching.

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
- `overwrite` — `RemoveAll` the destination and move the source into its
  place. Use only when you're certain the new copy supersedes the old one;
  this is the only mode that can lose data.

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

How the safety works:

- Every rule's `destination` is added to a protected set at startup.
- During matching, any path whose cleaned absolute path equals one of those
  destinations is skipped, no matter what its glob says.
- So a catch-all `globs: ["*"], kind: dir` rule cannot move `Installers/`,
  `Archives/`, or its own `Misc Folders/` — even though `*` would otherwise
  match them.
- Directory moves use `os.Rename` on the same filesystem, or a recursive
  copy+remove across devices. A partial cross-device copy is rolled back
  with `RemoveAll(dst)` on failure to avoid orphaned half-trees.

`kind: any` exists for the rare case where the same rule should apply to
both kinds of entry. The default is `file`, matching the original behavior.

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

- macOS: `~/Library/Logs/autoshelf.log` (matches the README's plist)
- Linux: `$XDG_STATE_HOME/autoshelf/autoshelf.log`, falling back to
  `~/.local/state/autoshelf/autoshelf.log`

Override with `--file`. `-f` / `--follow` tails the file the way `tail -f`
does — Ctrl-C exits cleanly.

**How the colored output works.** Under launchd / systemd, the daemon's
stderr is a regular file (not a TTY), so autoshelf writes JSON log records
to it — one per line, timestamp + level + msg + attribute pairs. When you
run `autoshelf log`, the command parses each JSON record, reconstructs a
`slog.Record` with the original timestamp and level intact, and feeds it
through [charmbracelet/log](https://github.com/charmbracelet/log) bound to
stdout. When stdout is a terminal you get colorized, level-tagged output;
when piped (`autoshelf log | grep moved`) you get clean text suitable for
filtering. Non-JSON lines — panics, anything that bypassed the structured
logger — pass through verbatim so nothing is silently dropped.

The same charm log formatter is used by `autoshelf run` directly when run
interactively in a terminal: in that case stderr IS a TTY, so colored text
is written to the screen instead of JSON.

## Running as a background service

If you installed via Homebrew, skip ahead — `brew services start autoshelf`
already covers this. The sections below are for users who want to manage the
service manually (no Homebrew, or a controlled environment where you write
your own unit files).

### macOS (launchd, manual)

**Before you load the plist, install the binary.** The plist below references
an absolute path — `/Users/USERNAME/.local/bin/autoshelf` — and launchd will
silently fail (exit code 78, empty log file) if nothing actually lives at that
path. `make build` only produces `./autoshelf` in the project directory; you
need `make install` to copy it into `~/.local/bin/`, or do it by hand:

```bash
make install
# or:
mkdir -p ~/.local/bin && cp ./autoshelf ~/.local/bin/autoshelf && chmod 0755 ~/.local/bin/autoshelf
```

Verify before continuing:

```bash
ls -la ~/.local/bin/autoshelf      # should exist, mode -rwxr-xr-x
file ~/.local/bin/autoshelf        # should be Mach-O for your arch
~/.local/bin/autoshelf validate    # should print "config OK: ..."
```

Then create `~/Library/LaunchAgents/com.angerops.autoshelf.plist`, replacing
`USERNAME` with your short username (it must match `~/.local/bin/autoshelf`'s
real owner — launchd does not expand `~` or env vars inside the plist):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.angerops.autoshelf</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/USERNAME/.local/bin/autoshelf</string>
        <string>run</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/USERNAME/Library/Logs/autoshelf.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/USERNAME/Library/Logs/autoshelf.log</string>
</dict>
</plist>
```

Load it:

```bash
launchctl load ~/Library/LaunchAgents/com.angerops.autoshelf.plist
```

Verify it's running (PID should be a real number, not `-`):

```bash
launchctl list | grep autoshelf
tail -f ~/Library/Logs/autoshelf.log
```

**Troubleshooting.** If `launchctl list | grep autoshelf` shows `-  78  com.angerops.autoshelf` and the log file is empty, launchd couldn't even execute the binary — almost always because the path in `ProgramArguments` doesn't point at a real file. Re-check `ls -la ~/.local/bin/autoshelf`. After fixing, restart cleanly with:

```bash
launchctl kickstart -k gui/$(id -u)/com.angerops.autoshelf
```

If the agent runs but doesn't move anything in `~/Downloads`, `~/Desktop`, or `~/Documents`, macOS is blocking it on **Full Disk Access**. Go to System Settings → Privacy & Security → Full Disk Access and add `/Users/USERNAME/.local/bin/autoshelf` (use Cmd-Shift-G in the picker to navigate to `.local/bin` since dotfolders are hidden), then `launchctl kickstart -k gui/$(id -u)/com.angerops.autoshelf` again.

### Linux (systemd --user, manual)

`~/.config/systemd/user/autoshelf.service`:

```ini
[Unit]
Description=autoshelf file organizer
After=default.target

[Service]
Type=simple
ExecStart=%h/.local/bin/autoshelf run
Restart=on-failure

[Install]
WantedBy=default.target
```

`systemctl --user enable --now autoshelf`

## Releasing (maintainer notes)

The binary builds are automated. Tagging the repo is the only manual step;
`.github/workflows/release.yml` takes it from there — runs the test suite,
cross-builds for darwin-arm64, darwin-amd64, linux-amd64, and linux-arm64,
packages each as `autoshelf-<tag>-<target>.tar.gz` containing the binary
plus `LICENSE` and `README.md`, generates a consolidated `checksums.txt`,
and creates the GitHub Release with everything attached.

### Cutting a release

1. Make sure `main` is green and the workflow file is committed (the
   workflow has to exist on the tagged commit).

2. Tag and push:

   ```bash
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

3. Watch GitHub Actions. When the `release` job finishes, the new release
   is live at `https://github.com/angerops/autoshelf/releases/tag/v0.1.0`
   with four `.tar.gz` archives and a `checksums.txt` attached, plus the
   auto-generated release notes from commits since the previous tag.

### Bumping the Homebrew formula

The formula installs the **pre-built binary** matching the user's OS + arch
from the release the workflow just produced, so no Go toolchain is needed
on the user's machine. There are four per-arch URL/sha256 pairs to bump on
every release. The four sha256 values are already in the release's
`checksums.txt`; grab them all with one curl:

```bash
curl -sL https://github.com/angerops/autoshelf/releases/download/v0.1.0/checksums.txt
# 0123…  autoshelf-v0.1.0-darwin-arm64.tar.gz
# abcd…  autoshelf-v0.1.0-darwin-amd64.tar.gz
# 4567…  autoshelf-v0.1.0-linux-arm64.tar.gz
# ef89…  autoshelf-v0.1.0-linux-amd64.tar.gz
```

Then copy `packaging/homebrew/autoshelf.rb` (the canonical formula in this
repo) to `angerops/homebrew-tap/Formula/autoshelf.rb`, updating in order:

1. `version "0.1.0"` — bump to the new tag (without the `v`).
2. The four `url "..."` lines — replace the version segment in each.
3. The four `sha256 "..."` lines — paste each digest into its matching
   `on_arm` / `on_intel` block.

Commit and push the tap. Users get the new version on their next
`brew upgrade autoshelf`.

The formula lives in this repo at `packaging/homebrew/autoshelf.rb` as the
source of truth; the tap repo is just a publishing target.

`brew install --HEAD angerops/tap/autoshelf` continues to work as a
build-from-source escape hatch for users who want to install off `main`
between releases. Only the `--HEAD` path requires Go.

## Layout

```
.
├── main.go
├── cmd/                 # cobra commands (root, run, once, validate, init, log)
├── internal/
│   ├── config/          # viper-backed YAML loader + validation
│   ├── rules/           # match + move engine
│   └── watcher/         # fsnotify watcher + debounce + live config reload
├── autoshelf.example.yaml
├── packaging/
│   └── homebrew/
│       └── autoshelf.rb # canonical Homebrew formula (copy to tap on release)
├── .github/
│   └── workflows/
│       └── release.yml  # CI: test + cross-build + GitHub Release on v* tag
├── Makefile
├── LICENSE              # 0BSD
└── go.mod
```

## Tests

```bash
make test          # or: go test ./... -race
```

The suite is race-clean and covers each layer:

- **config** — YAML load, normalization, defaults; validation of every
  required-field / unknown-value / unparseable-duration path; rule-level
  vs. watch-level `min_age` precedence; the viper-discovery regression
  that used to feed the compiled binary into the YAML parser.
- **rules** — glob matching (case-insensitive), all four `on_conflict`
  policies, file and directory moves including missing-destination
  creation and cross-device copy+remove, protected destinations for
  catch-all dir rules, symlink/device-node refusal, `min_age` deferral,
  `ignore_globs` at global and per-watch level (including the Safari
  `.download` directory traversal trap).
- **watcher** — debounce coalescing, pending-map cleanup, recursive
  subdir auto-add, deferred-entry requeueing — all over real fsnotify.
  Plus live config reload: file watch fires on in-place write, atomic
  rename, and late create with debounce; `ApplyConfig` swaps the engine
  atomically; pending timers re-resolve against the new cfg; concurrent
  schedule + reload is clean under the race detector.
- **cmd** — `log` (platform-specific default paths including the XDG
  override, JSON record rendering, non-JSON passthrough, follow-mode);
  `init` (the embedded sample matches the repo's example file
  byte-for-byte and round-trips through `Load`).

For exact coverage, run `go test -v ./...` and read the test names — they
describe what they verify.

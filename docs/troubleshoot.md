# Troubleshooting

Failure modes and fixes, grouped by the symptom you're seeing.

## Index

- [`launchctl list` shows the agent with exit code 78](#launchctl-list-shows-the-agent-with-exit-code-78)
- [Agent runs but doesn't move anything in `~/Downloads`, `~/Desktop`, or `~/Documents`](#agent-runs-but-doesnt-move-anything-in-downloads-desktop-or-documents)
- [A file appeared on a mounted share but autoshelf didn't pick it up](#a-file-appeared-on-a-mounted-share-but-autoshelf-didnt-pick-it-up)
- [I added a new config field but the running daemon ignores it](#i-added-a-new-config-field-but-the-running-daemon-ignores-it)
- [`autoshelf log` says the log file doesn't exist](#autoshelf-log-says-the-log-file-doesnt-exist)
- [`brew install` fails with SHA256 mismatch](#brew-install-fails-with-sha256-mismatch)

---

## `launchctl list` shows the agent with exit code 78

You see something like:

```
$ launchctl list | grep autoshelf
-  78  com.angerops.autoshelf
```

and the log file is empty.

**Cause.** Exit code 78 from launchd means it couldn't execute the
binary — almost always because the path in `ProgramArguments` doesn't
point at a real file. `make build` only produces `./autoshelf` in the
project directory; the plist references `/Users/USERNAME/.local/bin/autoshelf`,
which is a different location.

**Fix.**

```bash
ls -la ~/.local/bin/autoshelf
```

If that doesn't exist, install it:

```bash
make install
# or:
mkdir -p ~/.local/bin && cp ./autoshelf ~/.local/bin/autoshelf && chmod 0755 ~/.local/bin/autoshelf
```

Then restart the agent cleanly:

```bash
launchctl kickstart -k gui/$(id -u)/com.angerops.autoshelf
```

The PID column should now show a real number rather than `-`.

## Agent runs but doesn't move anything in `~/Downloads`, `~/Desktop`, or `~/Documents`

**Cause.** macOS is blocking the daemon on **Full Disk Access** (TCC).
Files dropped into `~/Downloads` are visible via stat but the daemon
gets EPERM when it tries to act on them. Since autoshelf doesn't error
loudly on permission denials (the watcher sees the event, the engine
can't move the file, logging it would create noise), the only visible
symptom is "nothing happens."

**Fix.** Grant Full Disk Access to the binary:

1. **System Settings → Privacy & Security → Full Disk Access**.
2. Click the `+` button.
3. In the file picker, press `Cmd-Shift-G` to type a path
   (`.local` is hidden in the default view).
4. Navigate to `/Users/USERNAME/.local/bin/autoshelf` and add it.
5. Restart the agent:

   ```bash
   launchctl kickstart -k gui/$(id -u)/com.angerops.autoshelf
   ```

If you installed via Homebrew the binary lives at
`$(brew --prefix)/bin/autoshelf` — grant Full Disk Access to that
path instead.

## A file appeared on a mounted share but autoshelf didn't pick it up

This is expected behavior when the file was written by a *different
machine*, not a bug. See [network-shares.md](network-shares.md) for
the underlying reason and the recommended workaround
(`autoshelf once` on a timer).

## I added a new config field but the running daemon ignores it

Two distinct cases:

1. **The field already existed in this binary's release.** The daemon
   has auto-reload — save the file and it picks the change up within
   a few hundred milliseconds. If it doesn't, check `autoshelf log`
   for a `config reload failed` line; usually the new YAML is
   malformed and the daemon kept the old config (which is the right
   behavior).

2. **The field was added in a newer version of autoshelf.** A field
   the running binary has never heard of is silently dropped on
   decode — auto-reload sees nothing to apply. `brew upgrade autoshelf`
   (or `make install` if running from source), then restart the
   agent.

If you're unsure which case applies, run `autoshelf --version` and
compare to the version that introduced the field.

## `autoshelf log` says the log file doesn't exist

`autoshelf log` probes several locations in priority order. The actual
search list:

1. `$HOMEBREW_PREFIX/var/log/autoshelf.log` (if `HOMEBREW_PREFIX` set)
2. `/opt/homebrew/var/log/autoshelf.log`
3. `/usr/local/var/log/autoshelf.log`
4. Platform default — macOS: `~/Library/Logs/autoshelf.log`; Linux:
   `$XDG_STATE_HOME/autoshelf/autoshelf.log` or
   `~/.local/state/autoshelf/autoshelf.log`

The error message points at the most likely location. If none of these
exist, the daemon probably hasn't started yet or its `StandardOutPath`
in the plist/unit points somewhere else. Override with `--file`:

```bash
autoshelf log --file /custom/path/autoshelf.log
```

## `brew install` fails with SHA256 mismatch

**Cause.** The formula's `sha256` value doesn't match the binary
archive on the release page. Most common reasons:

- The formula in the tap is stale (a release was cut but the formula
  wasn't bumped).
- A maintainer pasted a digest from the source tarball into a
  binary-install formula slot, or vice versa.
- The release archive was re-uploaded after the formula was published.

**Fix as a user:** wait or file an issue against the tap. The formula
in `angerops/homebrew-tap/Formula/autoshelf.rb` is the source brew
reads; comparing its `sha256` lines against the release's
`checksums.txt` will tell you which slot is wrong.

**Fix as the maintainer:** see the "Bumping the Homebrew formula"
section in [CONTRIBUTING.md](../CONTRIBUTING.md).

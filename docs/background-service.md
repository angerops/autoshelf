# Running autoshelf as a background service

If you installed via Homebrew, `brew services start autoshelf` already
handles this — brew generates the right launchd plist (macOS) or
systemd user unit (Linux) and supervises the daemon for you. This
document is for users who want to manage the service manually: no
Homebrew, or a controlled environment where you write your own unit
files.

For failure modes after the daemon is set up, see
[troubleshoot.md](troubleshoot.md).

## macOS (launchd, manual)

**Before you load the plist, install the binary.** The plist below
references an absolute path — `/Users/USERNAME/.local/bin/autoshelf` —
and launchd will silently fail (exit code 78, empty log file) if
nothing actually lives at that path. `make build` only produces
`./autoshelf` in the project directory; you need `make install` to
copy it into `~/.local/bin/`, or do it by hand:

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

Then create `~/Library/LaunchAgents/com.angerops.autoshelf.plist`,
replacing `USERNAME` with your short username (it must match
`~/.local/bin/autoshelf`'s real owner — launchd does not expand `~`
or env vars inside the plist):

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

After config or binary changes, restart cleanly:

```bash
launchctl kickstart -k gui/$(id -u)/com.angerops.autoshelf
```

If the agent doesn't start, doesn't move files, or behaves unexpectedly,
see [troubleshoot.md](troubleshoot.md).

## Linux (systemd --user, manual)

Create `~/.config/systemd/user/autoshelf.service`:

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

Enable and start:

```bash
systemctl --user enable --now autoshelf
```

Check status and tail the journal:

```bash
systemctl --user status autoshelf
journalctl --user -u autoshelf -f
```

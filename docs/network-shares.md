# Watching network shares

## Short version

autoshelf is a real-time watcher backed by kernel filesystem events
(kqueue on macOS, inotify on Linux). The kernel only emits events for
syscalls that happen on *this* machine. If another machine writes to a
share you have mounted locally, your kernel sees no syscall and
autoshelf sees no event.

For that case, run `autoshelf once` on a timer.

## What works in real time

Anything that involves a syscall on the machine running autoshelf:

- You save or drop a file into a folder that lives on local disk.
- You save or drop a file into a mounted SMB / NFS / AFP share *from
  this machine* — the syscall is local; the share's filesystem driver
  forwards it to the remote host.
- A sync client (Dropbox, iCloud Drive, Google Drive, OneDrive) writes
  into its synced folder. From the kernel's perspective the sync
  client is a local process doing local writes.

## What doesn't work in real time

- **Writes from another machine into the same share** — kernel sees no
  syscall, no event fires, autoshelf will not pick up the file until
  the next time something brings the watch around to it.

That's true for SMB, NFS, AFP, and any other network filesystem
client: the protocol doesn't push change notifications to the client's
kernel in a way kqueue/inotify can consume.

## The workaround: `autoshelf once` on a timer

`autoshelf once` runs the same rule engine in one-shot mode (initial
sweep, exit) and exits cleanly. Safe to fire from `cron`, a launchd
`StartInterval` plist, or a systemd timer.

Pick a cadence that matches how quickly you need files sorted — every
5 minutes is plenty for most "drop a file on the NAS" flows. Examples:

### cron (macOS / Linux)

```cron
*/5 * * * * /Users/USERNAME/.local/bin/autoshelf once
```

### launchd timer (macOS)

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.angerops.autoshelf.poll</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/USERNAME/.local/bin/autoshelf</string>
        <string>once</string>
    </array>
    <key>StartInterval</key>
    <integer>300</integer>
    <key>StandardOutPath</key>
    <string>/Users/USERNAME/Library/Logs/autoshelf.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/USERNAME/Library/Logs/autoshelf.log</string>
</dict>
</plist>
```

### systemd timer (Linux)

`~/.config/systemd/user/autoshelf-poll.service`:

```ini
[Unit]
Description=autoshelf periodic sweep

[Service]
Type=oneshot
ExecStart=%h/.local/bin/autoshelf once
```

`~/.config/systemd/user/autoshelf-poll.timer`:

```ini
[Unit]
Description=Run autoshelf sweep every 5 minutes

[Timer]
OnBootSec=1min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
```

```bash
systemctl --user enable --now autoshelf-poll.timer
```

## Mixing real-time and polling

You can do both — `autoshelf run` for local watches plus a separate
`autoshelf once` on a timer for remote watches. The simplest split is
two config files:

- `~/.config/autoshelf/autoshelf.yaml` — declares only the local
  watches; loaded by the `run` daemon.
- `~/.config/autoshelf/network.yaml` — declares only the network
  watches; loaded by the periodic `autoshelf once -c
  ~/.config/autoshelf/network.yaml` invocation.

The two processes are completely independent and don't interfere.

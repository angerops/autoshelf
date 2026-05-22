# Contributing to autoshelf

This document covers the things you need to know to build, test, and
release autoshelf. End-user documentation lives in [README.md](README.md).

## Building from source

Requires Go 1.22+.

```bash
make build                 # produces ./autoshelf for the current OS/arch
make build-darwin-arm64    # cross-build for Apple Silicon
make build-darwin-amd64    # cross-build for Intel macOS
make build-linux-amd64     # cross-build for x86_64 Linux
make build-linux-arm64     # cross-build for aarch64 Linux
make install               # installs to ~/.local/bin/autoshelf
make VERSION=v0.1.0 build  # stamp a specific version into the binary
```

`make build` uses `git describe --tags --dirty` to populate the
`--version` output. Pass `VERSION=...` to override.

## Repository layout

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
  policies (including every shape of `overwrite`: file-over-file,
  dir-over-dir, file-over-dir, dir-over-file, no-collision), file and
  directory moves including missing-destination creation and
  cross-device copy+remove, protected destinations for catch-all dir
  rules (held under `overwrite` too), symlink/device-node refusal,
  `min_age` deferral, `ignore_globs` at global and per-watch level
  (including the Safari `.download` directory traversal trap).
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

For exact coverage, run `go test -v ./...` and read the test names —
they describe what they verify.

## Releasing

The binary builds are automated. Tagging the repo is the only manual
step; `.github/workflows/release.yml` takes it from there — runs the
test suite, cross-builds for darwin-arm64, darwin-amd64, linux-amd64,
and linux-arm64, packages each as `autoshelf-<tag>-<target>.tar.gz`
containing the binary plus `LICENSE` and `README.md`, generates a
consolidated `checksums.txt`, and creates the GitHub Release with
everything attached.

### Cutting a release

1. Make sure `main` is green and the workflow file is committed (the
   workflow has to exist on the tagged commit).

2. Tag and push:

   ```bash
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

3. Watch GitHub Actions. When the `release` job finishes, the new
   release is live at
   `https://github.com/angerops/autoshelf/releases/tag/v0.1.0` with
   four `.tar.gz` archives and a `checksums.txt` attached, plus the
   auto-generated release notes from commits since the previous tag.

### Bumping the Homebrew formula

The formula installs the **pre-built binary** matching the user's OS +
arch from the release the workflow just produced, so no Go toolchain is
needed on the user's machine. There are four per-arch URL/sha256 pairs
to bump on every release. The four sha256 values are already in the
release's `checksums.txt`; grab them all with one curl:

```bash
curl -sL https://github.com/angerops/autoshelf/releases/download/v0.1.0/checksums.txt
# 0123…  autoshelf-v0.1.0-darwin-arm64.tar.gz
# abcd…  autoshelf-v0.1.0-darwin-amd64.tar.gz
# 4567…  autoshelf-v0.1.0-linux-arm64.tar.gz
# ef89…  autoshelf-v0.1.0-linux-amd64.tar.gz
```

Then copy `packaging/homebrew/autoshelf.rb` (the canonical formula in
this repo) to `angerops/homebrew-tap/Formula/autoshelf.rb`, updating in
order:

1. `version "0.1.0"` — bump to the new tag (without the `v`).
2. The four `url "..."` lines — replace the version segment in each.
3. The four `sha256 "..."` lines — paste each digest into its matching
   `on_arm` / `on_intel` block.

Commit and push the tap. Users get the new version on their next
`brew upgrade autoshelf`.

The formula lives in this repo at `packaging/homebrew/autoshelf.rb` as
the source of truth; the tap repo is just a publishing target.

`brew install --HEAD angerops/tap/autoshelf` continues to work as a
build-from-source escape hatch for users who want to install off `main`
between releases. Only the `--HEAD` path requires Go.

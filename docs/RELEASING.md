# Releasing sonar

The CLI ships as one static binary per platform, published as a GitHub release
by `.github/workflows/release.yml`. The Homebrew formula and the desktop app's
sidecar both follow from that release, so cutting one is a single tag.

## Before tagging

```sh
go build ./... && go vet ./... && go test ./...
go test -tags integration ./...
go generate ./... && git diff --exit-code docs/schema
scripts/readme-check.sh
```

The static builds the release makes, on the machine you are tagging from:

```sh
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 \
    go build -ldflags="-s -w -X github.com/raskrebs/sonar/internal/selfupdate.Version=vX.Y.Z" -o /tmp/sonar-check .
done
```

`CGO_ENABLED=0` has to hold on every platform: the database is
`modernc.org/sqlite`, which is pure Go, and a cgo build would tie the binary to
the libc of the runner that built it.

Check that the version is injected rather than hardcoded — `sonar version`
prints `dev` when the ldflag is missing, and the desktop app refuses a sidecar
whose version it cannot read:

```sh
go build -ldflags="-X github.com/raskrebs/sonar/internal/selfupdate.Version=v9.9.9" -o /tmp/sonar . && /tmp/sonar version
```

## Cutting the release

```sh
git tag -a vX.Y.Z -m "sonar vX.Y.Z"
git push origin vX.Y.Z
```

The tag triggers `release.yml`, which:

1. builds `linux/{amd64,arm64}`, `darwin/{amd64,arm64}` and
   `windows/{amd64,arm64}` with `-s -w` and the version ldflag;
2. ad-hoc codesigns the macOS binaries and builds the Swift tray
   (`tray/SonarTray.swift`) alongside them, so a macOS tarball holds `sonar`
   and `sonar-tray`;
3. publishes `sonar_<os>_<arch>.tar.gz` (and `.zip` on Windows) to the GitHub
   release with generated notes;
4. updates `raskrebs/homebrew-sonar` — the formula's version, the four
   SHA-256s and the download URLs — and pushes it, so `brew upgrade` serves the
   new build within a minute of the release finishing.

`scripts/install.sh` and `scripts/install.ps1` read the same release assets, so
nothing else has to be updated by hand.

## Release notes

Deprecations belong in the notes, because the notice the CLI prints is one line
and users see it once. For the group-and-daemon release, the two entries that
matter:

- `sonar up <name>` used to *check* whether a profile's ports were up; it now
  *starts* a group's services. That is the only behaviour change.
- `run`, `runs`, `profile`, `down`, `kill-all` and `--tag` still work and print
  what replaced them. They go away one minor release later.

## How the desktop app pins the CLI

The desktop app (`sonar-desktop`) bundles `sonar` as a Tauri sidecar and never
tracks `latest`: it pins one release in **`sidecar.lock.json`** at the root of
that repository — the CLI version plus a SHA-256 per target triple:

```json
{
  "version": "vX.Y.Z",
  "binaries": {
    "aarch64-apple-darwin": "sha256:…",
    "x86_64-apple-darwin": "sha256:…",
    "x86_64-pc-windows-msvc": "sha256:…",
    "x86_64-unknown-linux-gnu": "sha256:…",
    "aarch64-unknown-linux-gnu": "sha256:…"
  }
}
```

`scripts/fetch-sidecar.sh` there downloads the pinned assets, verifies the
checksums and writes `src-tauri/binaries/sonar-<triple>`, which Tauri bundles
next to the app executable. The app spawns it as `sonar serve --detach` when
nothing is already listening on the socket, and refuses to run against a daemon
newer than the CLI it knows.

So a CLI release reaches the app in two steps, in this order:

1. tag and release `sonar` here;
2. in `sonar-desktop`, bump `version` and the checksums in `sidecar.lock.json`,
   re-run `scripts/fetch-sidecar.sh`, and let its own release workflow bundle
   the app.

A CLI release that changes the protocol needs the schema regenerated in both
repositories (`go generate ./...` here, `pnpm sync-protocol` there) before the
app is bumped.

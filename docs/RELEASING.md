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

The relay is not part of the daemon protocol, so `go generate` says nothing
about it; its own gates are `go test ./internal/relay/...` and a
`docker build -f Dockerfile.relay .`.

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
   release with generated notes, alongside `sonar_checksums.txt`;
4. updates `raskrebs/homebrew-sonar` — the formula's version, the four
   SHA-256s and the download URLs — and pushes it, so `brew upgrade` serves the
   new build within a minute of the release finishing. It reads the SHA-256s
   out of `sonar_checksums.txt` rather than hashing the archives again, so the
   tap and the release cannot disagree.

`scripts/install.sh` and `scripts/install.ps1` read the same release assets, so
nothing else has to be updated by hand.

## The relay image

The same tag also builds `Dockerfile.relay` and pushes
`ghcr.io/raskrebs/sonar-relay:<tag>` and `:latest` for `linux/amd64` and
`linux/arm64`, from the `relay-image` job. It needs nothing from the build jobs
and publishes with the workflow's own `GITHUB_TOKEN` — the job asks for
`packages: write`, and there is no secret to rotate.

Both architectures are native `go build`s cross-compiled from the runner's own,
so the job has no QEMU and takes about as long as one build. To check it before
tagging:

```sh
docker build -f Dockerfile.relay -t sonar-relay .
docker run --rm -p 8787:8787 sonar-relay &
curl -fsS localhost:8787/healthz
```

The image is not part of the GitHub release and carries no checksum file: it is
addressed by digest in the registry. `docs/RELAY.md` covers deploying it.

## `sonar_checksums.txt`

Every release publishes one `sonar_checksums.txt` next to the archives: one
line per archive in `sha256sum`'s own format — `<sha256>` then two spaces then
the filename — sorted by filename. It covers exactly the six archives, and
nothing else: not itself, and not `sonar-tray`, which travels inside the darwin
tarballs.

The release job writes it after every build job's artifacts are downloaded and
before the assets are uploaded:

```sh
sha256sum sonar_*.tar.gz sonar_*.zip | LC_ALL=C sort -k2 > sonar_checksums.txt
```

To verify a download, put the file next to whatever you fetched and let
`sha256sum` check the ones you have:

```sh
curl -fsSLO https://github.com/raskrebs/sonar/releases/download/vX.Y.Z/sonar_checksums.txt
sha256sum -c --ignore-missing sonar_checksums.txt
```

`--ignore-missing` is what makes this work on a partial download: without it
`sha256sum` fails on the five archives you did not fetch. On macOS without GNU
coreutils, `shasum -a 256 -c --ignore-missing sonar_checksums.txt` does the
same.

`sonar remote install` verifies the same way without being asked:
`internal/remoteinstall` prefers the sha256 digest GitHub reports per asset and
falls back to this file for releases cut before those digests existed.
`internal/remoteinstall/testdata/sonar_checksums.txt` pins the format the
workflow produces, so a change to the shell above fails a test here first.

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

The SHA-256s to paste in are the archive lines of that release's
`sonar_checksums.txt` — the lock file pins the same hashes the release
published, so there is nothing to recompute.

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

# Fugue desktop app (ui module)

The Gio desktop app of Fugue, in its own Go module so that cgo and the
graphics stack never reach the core module (ADR 0002). The app talks to the
host only through the Host API, via the core module's public packages
`pkg/client` and `pkg/pb` — never an `internal/` package.

```sh
go build ./cmd/fugue-app   # from this directory
go test -race -shuffle=on -count=1 ./...
```

## Build requirements

Gio needs the platform graphics and input development packages.

**Ubuntu** (desktop-app §8, plus the three packages the F10.1 probe found
missing on Ubuntu 26.04: `libx11-xcb-dev`, `libxkbcommon-x11-dev`,
`libgles2-mesa-dev`):

```sh
sudo apt-get install -y --no-install-recommends \
  gcc pkg-config libwayland-dev libx11-dev libx11-xcb-dev \
  libxkbcommon-x11-dev libgles2-mesa-dev libegl1-mesa-dev \
  libffi-dev libxcursor-dev libvulkan-dev
```

**macOS**: the Xcode command line tools (`xcode-select --install`); Gio uses
Metal through cgo, no extra packages.

## Module rules

- `ui/go.mod` replaces `github.com/syurodev/fugue` with `../`; there is no
  `go.work`.
- The core module never imports this module, and this module imports the core
  only through `pkg/client` and `pkg/pb` (arch tests `TestCoreNeverImportsUI`
  and `TestAppWritesOnlyThroughHostAPI` in the core's `internal/arch`).
- The app opens no data-directory file, socket or SQLite path itself: the
  host owns them and `pkg/client` is the only way in.
- Fonts are bundled: Inter and JetBrains Mono with their OFL-1.1 license
  files (`internal/fonts/data/`), leading the shaper collection with system
  fonts only as the fallback (desktop-app DA-04).
- Goroutines here are owned by the window and the `hostconn` manager, every
  one with a stop path, and every package's `TestMain` runs goleak. The core
  module's `supervise` port is `internal/`, so this module carries the same
  discipline in its own packages.

## Window size

The initial size is the preferred 900x1000 dp clamped to the screen's work
area (F10.1 found 900x1000 dp overflowing an 864 dp-high 1080p screen at
scale 1.25). The clamp (`internal/appwin.ClampDp`) floors the pixel work area
through the scale into a dp budget and never goes below 360x240 dp. macOS
reads the work area from `NSScreen`; on Linux the compositor is the authority
— on Wayland and XWayland the granted size always fits (stage A has no native
X11 session, Q-32) — so the request is left unclamped there.

## CI

The `ui` workflow (`.github/workflows/ui.yml`) runs build, vet, lint and
`go test -race` for this module on ubuntu-latest and macos-latest, triggered
when a change touches `ui/`, `pkg/` or `proto/`. The core module's own gates
never build this module.

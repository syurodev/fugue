# Gio transcript spike (throwaway)

Measures whether Gio can render a Fugue-style transcript cheaply on a weak CPU,
and whether Vietnamese input works. It is the probe for roadmap item F10.1
(`desktop-app` DA-E01, DA-E02). Not product code: a separate Go module that no
gate builds; delete it when F10.1 is Done.

## Ubuntu prerequisites

```bash
sudo apt install gcc pkg-config libwayland-dev libx11-dev libx11-xcb-dev libxkbcommon-x11-dev libgles2-mesa-dev libegl1-mesa-dev libffi-dev libxcursor-dev libvulkan-dev
```

Go 1.27 or newer. The binary `giospike` is ignored by git.

## Run

```bash
cd spikes/gio
go build -o giospike .
./giospike -bench                                   # 2000 items, 600 chars/s stream
./giospike -bench -items 10000 -rate 3000 -stream-secs 10 -scroll-secs 6 -idle-secs 2
./giospike -items 40                                # manual: type Vietnamese (Telex via IBus) in the input box, press Enter
```

Each bench line reports frames, fps, CPU-side layout time (p50, p95, max),
frame gaps, CPU as % of one core, and peak RSS. Keep the window visible during
the bench; a hidden window stops receiving frames.

## Mac reference (M3 Pro, 11 cores, ProMotion 120 Hz)

```
open    fps=104.8 layout_p95=1.82ms cpu=22.0%core rss_max=157MB
stream  fps=120.0 layout_p95=2.72ms cpu=34.4%core rss_max=157MB
scroll  fps=120.0 layout_p95=2.87ms cpu=33.4%core rss_max=157MB
idle    fps=0.4                     cpu=0.3%core
10k items, 3000 ch/s: stream layout_p95=3.68ms cpu=42.2%core rss_max=198MB
```

## What to check by eye on Ubuntu

1. Vietnamese text renders with correct diacritics (fallback to system fonts).
2. Telex input through IBus: preedit shows while typing, committed text is right, backspace removes one character.
3. Scrolling with the wheel and touchpad feels smooth.

## Record the result

Paste the bench lines and the three checks above into the F10.1 PR (or the
roadmap log), with the machine (CPU, RAM, GPU), the session type (Wayland or
X11) and the input method framework (IBus or fcitx5).

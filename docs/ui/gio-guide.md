# Gio guide (layer 2 of the UI docs)

Status: **carried into kit v1**. This page started as the UI kit spike's
guide draft (`ui/internal/kitspike` on `spike/ui-kit-components`); F10.8
trimmed it to what is true of the kit as built and owns it from here. Each
section states what was verified on Gio v0.10.3 with real code, with one
minimal example. Sections marked **F10.9–F10.11** are verified knowledge
from the spike that those items will fold into component code; re-verify
when you touch them.

## 1. Props and state, keyed state (UK-02)

Every component is two values: a **props struct** the caller rebuilds each
frame, and a **state struct** the caller keeps for the lifetime of the
logical control. The state never points back at the props; the component
reads props and reports events out. Kit v1 so far has one state-ful
primitive, `kit.Spinner` (a phase angle); Button and TextField (F10.9,
F10.10) follow the same shape:

```go
type ButtonProps struct {
    Text     string
    Variant  ButtonVariant
    Busy     bool
    Disabled bool
}

type ButtonState struct{ click widget.Clickable }

func NewButtonState() *ButtonState { return &ButtonState{} }

// Layout draws and reports; UK-06: the event is read in the same frame.
func (s *ButtonState) Layout(env *kit.Env, gtx *layout.Context, p ButtonProps) bool { ... }
```

Rules that proved out:

- **In lists, keep state by the object's stable ID, never by index** — a map
  from ID to state, created lazily. The spike's gallery used fixed slices;
  Menu items and ListRows (F10.11, F10.12) need the map form.
- State is its own input tag: `&s.click`, `&s.ed`, or the state pointer
  (`event.Op(gtx.Ops, s)`). Never a recreated value — the address must be
  stable across frames or focus and gestures die.
- `gtx.Disabled()` returns a modified context: `d := gtx.Disabled()` stops a
  control from consuming input.

## 2. Events read during layout

Gio has no callbacks (UK-06): components drain events during their `Layout`
and return what happened.

```go
for {
    e, ok := s.ed.Update(*gtx) // widget.Editor: call until !ok
    if !ok {
        break
    }
    switch e.(type) {
    case widget.ChangeEvent:
        ev.Changed = true
    case widget.SubmitEvent:
        ev.Submitted = true
    }
}
```

- `widget.Clickable`: `Clicked(gtx)` drains pointer clicks **and** the
  Enter/Space key activations it registered itself.
- Focus transitions are polled, not pushed: compare `gtx.Focused(tag)` with
  last frame's value; on a drop, report `FocusLost`.
- Events queued between frames are matched against the filters registered in
  the *previous* frame, and `router.Frame` **discards** what is still
  pending. In tests: lay out, hand ops to the router, *then* queue, then lay
  out again.

## 3. Pointer: hover, clip, hit areas, cursors

- Hover and press come from `widget.Clickable`'s `Hovered()`/`Pressed()`; for
  raw areas register `event.Op(gtx.Ops, tag)` and read
  `pointer.Filter{Target: tag, Kinds: ...}`.
- **The input area of `event.Op` is the current transform AND clip.** Clip
  first: `clip.Rect(r).Push(gtx.Ops).Pop()` before the op.
- **Absorption (the big pitfall):** the hit-test walk stops at the topmost
  non-pass-through area under the pointer. An area with the whole window as
  its clip swallows every click beneath it — even if its handler reads no
  pointer filters at all. Kit's `Env.TrackInput` registers the window press
  listener *first* (at the back) for exactly this reason, and the kit's
  headless tests keep component areas small for the same one. Kit v1
  corollary (F10.11): a full-window catcher (menu outside-click, dialog
  scrim) must be the *topmost* area and receive everything not taken by
  areas above it.
- Cursor: `pointer.CursorColResize.Add(gtx.Ops)` — registered with the same
  clip as the area it belongs to.
- Pointer positions arrive in the **area's local coordinates** (inverse of
  the transform at registration).
- Minimum hit target (§3.4): give small glyphs a `theme.HitMin` box.

## 4. Focus and keyboard

Findings, all verified in the spike and re-verified for the kit:

- **Tab order = registration order** of input tags in the frame; a real app
  window turns Tab/Shift+Tab into `FocusForward`/`FocusBackward`
  automatically. Nothing to implement — just lay out in the visual order.
  (Note: `input.Router` alone does **not** synthesize the movement — the app
  window does. Kit's headless tests issue the `key.FocusCmd` themselves.)
- Three things make a tag focusable and focused:
  1. `event.Op(gtx.Ops, tag)` (registered, a.k.a. "visible");
  2. a `key.FocusFilter{Target: tag}` **read through `gtx.Event`** — this is
     the only thing that marks the handler focusable; a bare `key.Filter`
     with `Focus:` is not enough;
  3. focus set with `gtx.Execute(key.FocusCmd{Tag: tag})`.
- `widget.Clickable` does all of this itself, including Enter activating on
  press and Space on release (§6.2's rule for free).
- **Focus-visible has no Gio primitive.** The kit tracks the modality at the
  window: `Env.TrackInput` reads a Tab filter and turns "keyboard mode" on;
  any pointer press clears it; a control asks `Env.FocusVisible(gtx, tag)`
  and draws its ring only when both `gtx.Focused(tag)` and the modality hold
  (UK-05).
- Custom key handling: `key.Filter{Focus: tag, Name: key.NameReturn,
  Optional: ModShift | ...}`; `Required`/`Optional` are modifier masks, and
  the empty `Name` matches every key no other filter claimed.
- Escape: handle it where the focus is, and return focus with `key.FocusCmd`
  toward the opener (F10.11, §6.15).

## 5. The overlay layer (UK-07) — **F10.11**

One overlay owned by the window, laid out **last** in the frame:

```go
m := op.Record(gtx.Ops)
drawMenuStack()             // panels at window coordinates
op.Defer(gtx.Ops, m.Stop()) // paint + input on top of everything laid out after
```

- `op.Defer` puts the recorded ops (drawing *and* input) after the rest of
  the frame: the menu floats over content laid out later and its areas win.
- Anchors are **window coordinates**; Gio does not expose the current
  transform — F10.11 should thread a small offset stack through the layout
  helpers instead of the spike's manual bookkeeping.
- Flipping at the window edge is arithmetic on the measured panel rect
  (`measure → place → flip if beyond edge`), done every frame.
- Menu state per level: items, clickables, `motion.Presence` for open/close,
  keyboard highlight, typeahead buffer. `Presence` gives "turn round
  mid-way" for free (open with Snappy, close with Exit).
- Diagonal travel: keep the submenu open while the pointer is not hovering a
  different parent item (a trapezoid corridor is a refinement).

## 6. Text: shaper, line height, Editor, IME

- Absolute line height per type token (§3.0): set both
  `LineHeight: token.Line` and `LineHeightScale: 1` on `widget.Label`,
  `widget.Editor` and `text.Parameters`. Gio's default is 1.2× with a scale
  factor; leaving either field at zero silently reverts. The kit does this
  in one place (`kit.Label`/`kit.Paragraph`); `type.codeInline` is the one
  token with no absolute height — its line belongs to the containing prose
  run.
- Vietnamese stress string `Ễ Ặ Ỡ Ậ ỷ ộ` at every token, two lines: no
  overlap between lines and no clipping of stacked marks (UK-E08,
  `TestEveryTypeTokenKeepsStackedMarksInsideTheLine`); the poke above the
  line top stays under 1 dp.
- One editing core (UK-08) is `widget.Editor` with a caller-drawn box:

```go
m := op.Record(gtx.Ops)
paint.ColorOp{Color: fg}.Add(gtx.Ops)
textMat := m.Stop()
sm := op.Record(gtx.Ops)
paint.ColorOp{Color: selection}.Add(gtx.Ops)
selMat := sm.Stop()
dims := s.ed.Layout(*gtx, shaper, face, size, textMat, selMat)
```

  The Editor brings select/copy/cut/paste/undo/redo shortcuts, masking
  (`Mask: '•'`), single line + submit, and the IME plumbing. **F10.10**.
- IME (verified on macOS Simple Telex with real keycodes in the spike):
  `aa` → `â`, `vieets␣` → `viết␣`; a backspace after the commit deletes
  exactly one character. Two things a kit needs to know: (1) after an IME
  commit the caret sits at the range **start** — the IME then sends a
  `key.SelectionEvent` to place it; tests that simulate the IME must do the
  same; (2) with macOS spelling check on, a word only commits at a boundary
  — do not test with the caret mid-word.

## 7. Icons

The kit packages Lucide path data (`ui/internal/kit/icondata`) with its ISC
license file alongside. The committed generator (`icondata_test.go`, run
with `-update`) converts the original SVG data — every arc and quadratic
becomes a cubic, the standard endpoint-to-center conversion — and writes
`icondata.go`; the renderer only ever sees M/L/C/Z. Adding an icon is a line
in `kit`'s name table, a file in `icondata/testdata`, and one `-update` run.

Two render paths measured over a 100-icon 16 dp grid (spike):

- (a) fresh `clip.Path` + `clip.Stroke` per frame: **4.53 ms**
- (b) stroke built once, transformed per frame: **1.06 ms**

The kit does (b), with one hard rule: **a `clip.PathSpec` lives in the
`op.Ops` buffer it was recorded in**. `Env` owns a never-reset buffer and
caches one stroke per (icon, dp size) — the cache key is dp, never device
pixels, and the affine transform at draw time carries the 24-grid path to
the current metric (`TestIconStrokeCacheIsKeyedByDpSize`). Sharpness is
identical between (a) and (b) — same geometry — at 12/14/16/20 dp and pixel
scale 1 and 1.25.

## 8. Animation frames with motion

The F10.6 pattern, wrapped by the kit's Env: `env.Begin(gtx.Now)` at frame
start (advances the driver), `env.Keep()` from every animation that still
moves, `env.FrameEnd(gtx)` at the end (schedules the invalidate). Measured
on the spike's gallery:

- idle pages: **0 frames** beyond the first in 120 ticks (a spinner keeps
  requesting while drawn — `TestSpinnerRequestsFramesOnlyWhileDrawn`);
- menu: open 26 frames, submenu 25, close 21 at 60 Hz — a naive
  always-drawing menu would spend 60 per second.

One trap: the frame that *processes* the close only retargets the spring; the
movement starts the next frame. Tests that count frames until "nothing
moves" need two consecutive quiet frames.

## 9. Virtualized lists

Not built in the spike — `transcriptview` (F10.4a) is the reference
implementation; the kit's VirtualList (F10.12) generalizes it (§6.26). The
one transferable lesson: **read shared fields under their lock**, and cache
heights per object key, never per revision.

## 10. Headless tests and screenshots

The pattern for everything above, no GPU in unit tests:

```go
r := new(input.Router)
gtx := layout.Context{
    Ops:         new(op.Ops),
    Constraints: layout.Exact(image.Pt(400, 300)),
    Source:      r.Source(),
}
// frame 1: lay out, then hand ops to the router
r.Frame(gtx.Ops)
// queue events, then lay out again — see §2 for the ordering rules
r.Queue(key.Event{Name: key.NameReturn, State: key.Press})
```

PNG screenshots come from `gioui.org/gpu/headless`: `headless.NewWindow(w,
h)`, `win.Frame(&ops)`, `win.Screenshot(img)`. The spike's shooter drove the
real input path so the shots show true hover, focus and menu states. The
gallery (F10.9) owns the shot matrix from here.

## 11. Pitfalls (all hit and fixed; each worth remembering)

1. **Pointer absorption**: any non-pass input area stops the hit-test walk; an
   unclipped `event.Op` swallows the window (§3, §5).
2. **PathSpec lifetime**: a `clip.PathSpec` is bound to the `op.Ops` it was
   recorded in; cache strokes only in a never-reset buffer (§7).
3. **Focusability**: `key.Filter{Focus: t}` alone does not make a tag
   focusable — a read of `key.FocusFilter{Target: t}` does (§4).
4. **Frame/event ordering**: `router.Frame` discards pending events; filters
   registered in frame N match events queued before frame N+1 (§2, §10).
5. **Command timing**: a `FocusCmd` issued during the opening frame can lose
   the race with the opener's own focus handling — expect a one-frame delay
   in tests (§5).
6. **Zero-time traps**: `gtx.Now` is zero unless set; double-click windows and
   press histories silently never fire (tests must set it from their clock).
7. **Empty-name key filters** (`Name: ""`) match every key no other filter
   claimed — a menu typeahead filter must not live at the window level or it
   eats shortcuts.
8. **`pointer.Drag` cannot be queued** in tests (v0.10 made Kind a bitmask and
   Drag is synthesized from Move while pressed); queue `Move` with `Buttons`.
9. **Sprite/image init order**: package-level `paint.NewImageOp` captures the
   sprite before `init()` fills it; build lazily via `sync.Once`.
10. **UI scale** (§3.0) is a one-line metric override
    (`kit.Env.Metric` multiplies `PxPerDp` and `PxPerSp` by the same
    factor); every spike page laid out and rendered at 90/100/110/125% with
    nothing cached in pixels — the only pixel-cache risk (icon strokes) keys
    on dp size, which stays correct.

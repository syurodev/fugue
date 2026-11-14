# Gio guide (draft from the kit spike)

Status: **draft**, written from the UI kit spike (`ui/internal/kitspike`,
`ui/cmd/kitspike` on `spike/ui-kit-components`). Everything here was done, not
read: each section says what the spike actually did on Gio v0.10.3, with one
minimal example. Kit v1 carries this over into the kit's doc comments. The
spike's code is throwaway; this page is the keeper.

## 1. Props and state, keyed state (UK-02)

Every component is two values: a **props struct** the caller rebuilds each
frame, and a **state struct** the caller keeps for the lifetime of the logical
control. The state never points back at the props; the component reads props
and reports events out.

```go
type ButtonProps struct {
    Text     string
    Variant  ButtonVariant
    Size     ButtonSize
    Busy     bool
    Disabled bool
}

type ButtonState struct{ click widget.Clickable }

func NewButtonState() *ButtonState { return &ButtonState{} }

// Layout draws and reports; UK-06: the event is read in the same frame.
func (s *ButtonState) Layout(env *Env, gtx *layout.Context, p ButtonProps) bool { ... }
```

Rules that proved out:

- **In lists, keep state by the object's stable ID, never by index** — a map
  from ID to `*ButtonState`, created lazily. The spike's gallery pages used
  fixed slices; kit v1 needs the map form for Menu items and ListRows.
- State is its own input tag: `&s.click`, `&s.ed`, or the state pointer
  (`event.Op(gtx.Ops, s)`). Never a recreated value — the address must be
  stable across frames or focus and gestures die.
- `gtx.Disabled()` (returns a modified copy) is how a disabled control stops
  consuming input: `d := gtx.Disabled(); s.click.Layout(d, ...)`.

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
- Focus transitions are polled, not pushed: compare
  `focused := gtx.Focused(tag)` with last frame's value; on a drop, report
  `FocusLost`.
- Events queued between frames are matched against the filters registered in
  the *previous* frame, and `router.Frame` **discards** what is still pending.
  In tests: lay out, hand ops to the router, *then* queue, then lay out again.

## 3. Pointer: hover, clip, hit areas, cursors

- Hover and press come from `widget.Clickable`'s `Hovered()`/`Pressed()`; for
  raw areas register `event.Op(gtx.Ops, tag)` and read
  `pointer.Filter{Target: tag, Kinds: ...}`.
- **The input area of `event.Op` is the current transform AND clip.** Clip
  first: `clip.Rect(r).Push(gtx.Ops).Pop()` before the op.
- **Absorption (the big pitfall):** the hit-test walk stops at the topmost
  non-pass-through area under the pointer. An area with the whole window as
  its clip swallows every click beneath it — even if its handler reads no
  pointer filters at all. The spike's menu scrim never saw clicks until the
  menu's own `event.Op` was clipped to the panel rect. Kit v1 corollary: a
  full-window catcher (menu outside-click, dialog scrim) must be the
  *topmost* area, and design its handler to receive everything not taken by
  areas above it.
- Cursor: `pointer.CursorColResize.Add(gtx.Ops)` — registered with the same
  clip as the area it belongs to.
- Pointer positions arrive in the **area's local coordinates** (inverse of the
  transform at registration). The overlay drew at the window origin, so menu
  positions were window coordinates directly.
- Minimum hit target (§3.4): give small glyphs a `size.hit.min` clickable box.

## 4. Focus and keyboard

Findings, all verified in the spike:

- **Tab order = registration order** of input tags in the frame; the window
  turns Tab/Shift+Tab into `FocusForward`/`FocusBackward` automatically.
  Nothing to implement — just lay out in the visual order.
- Three things make a tag focusable and focused:
  1. `event.Op(gtx.Ops, tag)` (registered, a.k.a. "visible");
  2. a `key.FocusFilter{Target: tag}` **read through `gtx.Event`** — this is
     the only thing that marks the handler focusable; a bare `key.Filter` with
     `Focus:` is not enough;
  3. focus set with `gtx.Execute(key.FocusCmd{Tag: tag})`.
- `widget.Clickable` does all of this itself, including Enter activating on
  press and Space on release (§6.2's rule for free).
- **Focus-visible has no Gio primitive.** Track the modality at the window:
  a window-level `key.Filter{Name: key.NameTab, Optional: ModShift|...}` sets
  "keyboard mode"; any pointer `Press` clears it; the ring draws only when
  `gtx.Focused(tag) && keyboardMode` (UK-05, ~40 lines in the spike's `Env`).
- Custom key handling: `key.Filter{Focus: tag, Name: key.NameReturn,
  Optional: ModShift | ...}`; `Required`/`Optional` are modifier masks, and
  the empty `Name` matches every key no other filter claimed.
- Escape: handle it where the focus is (menu panel), and return focus with
  `key.FocusCmd` toward the opener (§6.15).

## 5. The overlay layer (UK-07)

One overlay owned by the window, laid out **last** in the frame:

```go
m := op.Record(gtx.Ops)
drawMenuStack()            // panels at window coordinates
op.Defer(gtx.Ops, m.Stop()) // paint + input on top of everything laid out after
```

- `op.Defer` puts the recorded ops (drawing *and* input) after the rest of the
  frame: the menu floats over content laid out later and its areas win.
- Anchors are **window coordinates**; the caller computes them from its own
  known offsets. Gio does not expose the current transform — kit v1 should
  thread a small offset stack through the layout helpers instead of the
  spike's manual bookkeeping.
- Flipping at the window edge is arithmetic on the measured panel rect
  (`measure → place → flip if beyond edge`), done every frame.
- Menu state per level: items, clickables, `motion.Presence` for open/close,
  keyboard highlight, typeahead buffer. `Presence` gives "turn round mid-way"
  for free (open with Snappy, close with Exit).
- Submenu: at most two levels (UK-07); the third must be a dialog. Keyboard
  focus moves *into* the submenu on Right and back to the parent on Left or
  Escape.
- Diagonal travel: keep the submenu open while the pointer is not hovering a
  different parent item (a trapezoid corridor is a kit-v1 refinement).

## 6. Text: shaper, line height, Editor, IME

- Absolute line height per type token (§3.0): set both
  `LineHeight: token.Line` and `LineHeightScale: 1` on `widget.Label`,
  `widget.Editor` and `text.Parameters`. Gio's default is 1.2× with a scale
  factor; leaving either field at zero silently reverts.
- Vietnamese stress string `Ễ Ặ Ỡ Ậ ỷ ộ` at every token: no overlap between
  lines at any §3.3 token (worst gap 0.45 dp at `small` 12/16) and no
  clipping of stacked marks; `caption` pokes 0.05 dp above its box. The
  measured table is in the spike report; `go run ./cmd/kitspike
  -type-metrics` prints it.
- One editing core (UK-08) is just `widget.Editor` with a caller-drawn box:

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
  (`Mask: '•'`), single line + submit, and the IME plumbing.
- IME (verified on macOS Simple Telex with real keycodes, screenshots in
  `cmd/kitspike/shots/ime/`): `aa` → `â`, `vieets␣` → `viết␣`; a backspace
  after the commit deletes exactly one character. Two things a kit needs to
  know: (1) after an IME commit the caret sits at the range **start** — the
  IME then sends a `key.SelectionEvent` to place it; tests that simulate the
  IME must do the same; (2) with macOS spelling check on, a word only commits
  at a boundary — do not test with the caret mid-word.
- Ubuntu/IBus checklist for the owner: see the report, §"Ubuntu checks".

## 7. Icons

Ten Lucide icons packaged as path data (`testdata/*.txt`, ISC license file
alongside). Parse SVG path data — M/L/H/V/C/S/Q/T/A/Z, relative and absolute —
into lines and cubics; **convert arcs to cubics** while parsing
(endpoint-to-center parameterization, ≤90° per segment): Gio has `ArcTo` but
its semantics differ from SVG's, and the cubic conversion is the standard
every renderer runs.

Two render paths measured over a 100-icon 16 dp grid, layout+render p50:

- (a) fresh `clip.Path` + `clip.Stroke` per frame: **4.53 ms**
- (b) stroke built once, transformed per frame: **1.06 ms**

Recommend (b), with one hard rule: **a `clip.PathSpec` lives in the `op.Ops`
buffer it was recorded in**. Build the cached strokes in a package-level
buffer that is never reset, or the first `ops.Reset()` invalidates them (the
spike crashed in the GPU decode before learning this). Scale to the target
size with `op.Affine`, choosing the stroke width so it lands on §3.4's
`stroke.icon` after the transform. Sharpness is identical between (a) and (b)
— same geometry — at 12/14/16/20 dp and pixel scale 1 and 1.25.

## 8. Animation frames with motion

Reuse the F10.6 pattern unchanged: `motion.Driver.Begin(now)` at frame start,
`Keep(moving)` from every animation, `gtx.Execute(op.InvalidateCmd{At: at})`
at the end. Measured on the gallery (Q7):

- idle pages: **0 frames** beyond the first in 120 ticks (busy spinner cells
  legitimately keep requesting — they stop when undrawn);
- menu: open 26 frames, submenu 25, close 21 at 60 Hz — a naive
  always-drawing menu would spend 60 per second.

One trap: the frame that *processes* the close only retargets the spring; the
movement starts the next frame. Tests that count frames until "nothing moves"
need two consecutive quiet frames.

## 9. Virtualized lists

Not built in the spike — `transcriptview` (F10.4a) is the reference
implementation; the kit's VirtualList generalizes it (§6.26). The one
transferable lesson this spike re-learned: **read shared fields under their
lock** (F10.4a's race), and cache heights per object key, never per revision.

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

PNG screenshots come from `gioui.org/gpu/headless`: `headless.NewWindow(w, h)`,
`win.Frame(&ops)`, `win.Screenshot(img)`. The spike's shooter drives the real
input path (`input.Router` + key/pointer events) so the shots show true hover,
focus and menu states (`cmd/kitspike/headless.go`). The full matrix plus the
IME evidence lives in `cmd/kitspike/shots/` with `index.md`.

## 11. Pitfalls hit (all fixed in the spike, all worth a kit-v1 arch test or doc note)

1. **Pointer absorption**: any non-pass input area stops the hit-test walk; an
   unclipped `event.Op` swallows the window (§3, §5).
2. **PathSpec lifetime**: a `clip.PathSpec` is bound to the `op.Ops` it was
   recorded in; cache strokes only in a never-reset buffer (§7).
3. **Focusability**: `key.Filter{Focus: t}` alone does not make a tag
   focusable — a read of `key.FocusFilter{Target: t}` does (§4).
4. **Frame/event ordering**: `router.Frame` discards pending events; filters
   registered in frame N match events queued before frame N+1 (§2, §10).
5. **Command timing**: a `FocusCmd` issued during the opening frame can lose
   the race with the opener's own focus handling — grab once per frame while
   unfocused, and expect a one-frame delay in tests (§5).
6. **Zero-time traps**: `gtx.Now` is zero unless set; double-click windows and
   press histories silently never fire (§10 harness sets it from the rig
   clock).
7. **Empty-name key filters** (`Name: ""`) match every key no other filter
   claimed — a menu typeahead filter must not live at the window level or it
   eats shortcuts.
8. **`pointer.Drag` cannot be queued** in tests (v0.10 made Kind a bitmask and
   Drag is synthesized from Move while pressed); queue `Move` with `Buttons`.
9. **Sprite/image init order**: package-level `paint.NewImageOp(shadowSprite)`
   captured the sprite before `init()` filled it; build lazily via
   `sync.Once` (Q6).
10. **UI scale** (Q8) is a one-line metric override
    (`unit.Metric{PxPerDp: px * ui, PxPerSp: px * ui}`); every page laid out
    and rendered at 90/100/110/125% with nothing cached in pixels — the only
    pixel-cache in the spike (icon strokes) keys on dp size, which stays
    correct.

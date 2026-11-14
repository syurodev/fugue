package main

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestEveryPageLaysOutAtEveryScale guards the whole gallery against layout
// panics and GPU frame failures across the §3.0 UI scales.
func TestEveryPageLaysOutAtEveryScale(t *testing.T) {
	for _, us := range []float32{0.9, 1.0, 1.1, 1.25} {
		for _, light := range []bool{false, true} {
			r, err := newRig(light, 1, us)
			if err != nil {
				t.Fatal(err)
			}
			for i := range pageNames {
				r.g.pages.cur = pageID(i)
				if err := r.layout(); err != nil {
					t.Fatalf("uiscale %.2f light=%v page %s: %v", us, light, pageNames[i], err)
				}
			}
			r.release()
		}
	}
}

// TestMenuScript opens, walks, opens the submenu and escapes — the input
// path the shots use — and requires the menu to end closed.
func TestMenuScript(t *testing.T) {
	r, err := newRig(false, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.release()
	if err := shootMenu(r, t.TempDir(), "test"); err != nil {
		t.Fatal(err)
	}
	if o := r.g.pages.overlay; o.Shown() {
		t.Fatal("menu still shown after the script's escapes")
	}
}

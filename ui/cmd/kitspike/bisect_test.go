package main

import (
	"testing"
)

func TestBisectPages(t *testing.T) {
	for _, us := range []float32{0.9, 1.0, 1.1, 1.25} {
		for i := range pageNames {
			r, err := newRig(false, 1, us)
			if err != nil {
				t.Fatal(err)
			}
			for j := 0; j <= i; j++ {
				r.g.pages.cur = pageID(j)
				if err := r.layout(); err != nil {
					t.Fatalf("us %v page %d: %v", us, j, err)
				}
			}
			r.release()
		}
	}
}

package p

import "testing"

func FuzzParse(f *testing.F) {
	f.Add("boom")
	f.Fuzz(func(_ *testing.T, s string) { Parse(s) })
}

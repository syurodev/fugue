// Package icondata holds the packaged Lucide icon path data (ui-kit §6.1,
// §10): one constant per icon, converted to the M/L/C/Z subset — every arc
// and quadratic of the original SVG data became cubics — so the kit's
// renderer never sees an arc command. The original Lucide data it was
// generated from lives in testdata next to the ISC license, and icondata_test.go
// is the generator committed with its output: run
//
//	go test ./internal/kit/icondata -run TestDataIsCurrent -update
//
// after adding or changing an icon to rewrite icondata.go.
//
// Owner: F10.8. Allowed callers: ui/internal/kit only.
package icondata

package a

func A() {
	f() //nolint:errcheck // reason one
	g() //nolint:errcheck // reason two
	h() //nolint:gosec // reason three
}

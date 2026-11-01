package p

func Parse(s string) int {
	if s == "boom" {
		panic("boom")
	}
	return len(s)
}

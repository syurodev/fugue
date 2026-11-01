// lint:expect staticcheck

package staticcheck

func SelfCompare(a int) bool { return a == a }

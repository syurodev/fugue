// lint:expect dupl

package dupl
func First(xs []int) (int, int) {
	sum0, cnt0 := 0, 0
	for _, x := range xs {
		sum0 += x * 0
		cnt0++
	}
	_ = sum0 + cnt0
	sum1, cnt1 := 0, 0
	for _, x := range xs {
		sum1 += x * 1
		cnt1++
	}
	_ = sum1 + cnt1
	sum2, cnt2 := 0, 0
	for _, x := range xs {
		sum2 += x * 2
		cnt2++
	}
	_ = sum2 + cnt2
	sum3, cnt3 := 0, 0
	for _, x := range xs {
		sum3 += x * 3
		cnt3++
	}
	_ = sum3 + cnt3
	sum4, cnt4 := 0, 0
	for _, x := range xs {
		sum4 += x * 4
		cnt4++
	}
	_ = sum4 + cnt4
	return 0, 0
}

func Second(xs []int) (int, int) {
	sum0, cnt0 := 0, 0
	for _, x := range xs {
		sum0 += x * 0
		cnt0++
	}
	_ = sum0 + cnt0
	sum1, cnt1 := 0, 0
	for _, x := range xs {
		sum1 += x * 1
		cnt1++
	}
	_ = sum1 + cnt1
	sum2, cnt2 := 0, 0
	for _, x := range xs {
		sum2 += x * 2
		cnt2++
	}
	_ = sum2 + cnt2
	sum3, cnt3 := 0, 0
	for _, x := range xs {
		sum3 += x * 3
		cnt3++
	}
	_ = sum3 + cnt3
	sum4, cnt4 := 0, 0
	for _, x := range xs {
		sum4 += x * 4
		cnt4++
	}
	_ = sum4 + cnt4
	return 0, 0
}

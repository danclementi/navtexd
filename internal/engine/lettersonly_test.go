package engine

import "testing"

func TestIsChinese(t *testing.T) {
	for hz, want := range map[float64]bool{486000: true, 486500: true, 490000: false, 518000: false, 424000: false, 4209500: false} {
		if isChinese(hz) != want {
			t.Errorf("isChinese(%v) = %v", hz, !want)
		}
	}
}

package sysinfo

import (
	"testing"
	"time"
)

func TestCPUMeter(t *testing.T) {
	var m CPUMeter
	m.Percent()
	end := time.Now().Add(150 * time.Millisecond)
	for x := 0; time.Now().Before(end); x++ {
	}
	if p := m.Percent(); p <= 0 || p > 100 {
		t.Errorf("cpu percent %.2f", p)
	}
}

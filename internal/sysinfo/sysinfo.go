// Package sysinfo reports the process's own resource use.
package sysinfo

import (
	"runtime"
	"time"
)

// CPUMeter measures this process's CPU use as a percentage of the whole machine (all cores) between calls.
type CPUMeter struct {
	lastCPU  time.Duration
	lastWall time.Time
}

// Percent returns the CPU use since the previous call (0 on the first call or on error).
func (m *CPUMeter) Percent() float64 {
	cpu, err := processCPU()
	now := time.Now()
	defer func() { m.lastCPU, m.lastWall = cpu, now }()
	if err != nil || m.lastWall.IsZero() {
		return 0
	}
	wall := now.Sub(m.lastWall)
	if wall <= 0 {
		return 0
	}
	p := 100 * float64(cpu-m.lastCPU) / (float64(wall) * float64(runtime.NumCPU()))
	if p < 0 {
		p = 0
	}
	return p
}

// SystemMeter measures the CPU use of the whole machine (all programs, all cores) between calls.
type SystemMeter struct {
	lastBusy, lastTotal uint64
	have                bool
}

// Percent returns the machine's CPU use since the previous call; ok is false when it cannot be measured (first call,
// unsupported system).
func (m *SystemMeter) Percent() (p float64, ok bool) {
	busy, total, err := systemTimes()
	if err != nil {
		return 0, false
	}
	defer func() { m.lastBusy, m.lastTotal, m.have = busy, total, true }()
	if !m.have || total <= m.lastTotal {
		return 0, false
	}
	return 100 * float64(busy-m.lastBusy) / float64(total-m.lastTotal), true
}

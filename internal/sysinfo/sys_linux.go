//go:build linux

package sysinfo

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

// systemTimes returns the busy and total CPU time (clock ticks, all cores) of the whole machine so far.
func systemTimes() (busy, total uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, errors.New("no /proc/stat")
	}
	fs := strings.Fields(sc.Text())
	if len(fs) < 5 || fs[0] != "cpu" {
		return 0, 0, errors.New("unexpected /proc/stat")
	}
	var v []uint64
	for _, s := range fs[1:] {
		n, e := strconv.ParseUint(s, 10, 64)
		if e != nil {
			break
		}
		v = append(v, n)
	}
	for i, n := range v {
		if i < 8 { // user nice system idle iowait irq softirq steal
			total += n
		}
	}
	idle := v[3]
	if len(v) > 4 {
		idle += v[4]
	}
	return total - idle, total, nil
}

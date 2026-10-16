//go:build !windows && !linux

package sysinfo

import "errors"

func systemTimes() (busy, total uint64, err error) { return 0, 0, errors.New("not supported") }

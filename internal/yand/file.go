package yand

import (
	"bufio"
	"fmt"
	"os"

	"navtex/internal/store"
)

// ReadFile reads a YaND database or reception report from a file (recognized by its contents) and says in words what it found.
func ReadFile(name string) ([]store.Record, string, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	head := make([]byte, 8192)
	n, _ := f.ReadAt(head, 0)
	switch Kind(head[:n]) {
	case "abs":
		recs, st, err := ReadABS(f, fi.Size())
		if err != nil {
			return nil, "", err
		}
		return recs, fmt.Sprintf("YaND database: %d records, %d without text, %d with text taken from the neighbourhood", st.Rows, st.NoText, st.Mismatch), nil
	case "report":
		recs, err := ReadReport(bufio.NewReader(f))
		if err != nil {
			return nil, "", err
		}
		return recs, fmt.Sprintf("YaND reception report: %d messages", len(recs)), nil
	}
	return nil, "", fmt.Errorf("not a YaND database (YAND_DB.ABS) or reception report")
}

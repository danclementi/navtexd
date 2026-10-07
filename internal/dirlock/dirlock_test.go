package dirlock

import (
	"errors"
	"testing"
)

func TestSecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	a, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Acquire = %v, want ErrBusy", err)
	}
	a.Release()
	b, err := Acquire(dir)
	if err != nil {
		t.Fatalf("after Release: %v", err)
	}
	b.Release()
}

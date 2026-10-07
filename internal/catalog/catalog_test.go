package catalog

import (
	"path/filepath"
	"testing"

	"navtex/internal/store"
)

func TestSelectiveLoggingPersists(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Logged(5) || !c.Logged(0) {
		t.Fatal("everything is logged by default")
	}
	if err := c.SetDisabled([]int{5, 7}); err != nil {
		t.Fatal(err)
	}
	if c.Logged(5) || c.Logged(7) || !c.Logged(6) || !c.Logged(0) {
		t.Error("disabled set not applied")
	}
	c2, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	if got := c2.Disabled(); len(got) != 2 || got[0] != 5 || got[1] != 7 {
		t.Errorf("not persisted: %v", got)
	}
	if err := c2.SetDisabled(nil); err != nil || len(c2.Disabled()) != 0 {
		t.Errorf("check all: %v %v", err, c2.Disabled())
	}
	if _, err := filepath.Glob(filepath.Join(dir, "*")); err != nil {
		t.Fatal(err)
	}
}

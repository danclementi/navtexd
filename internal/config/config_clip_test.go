package config

import "testing"

func TestReview_ClipMemoryLimit(t *testing.T) {
	c := Default()
	if c.Clips.MaxMemoryMB != 500 {
		t.Fatalf("default %d", c.Clips.MaxMemoryMB)
	}
	c.Clips.MaxMemoryMB = 0
	if err := c.Validate(); err == nil {
		t.Fatal("0 MB accepted")
	}
}

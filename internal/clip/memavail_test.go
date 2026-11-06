package clip

import (
	"runtime"
	"testing"
)

func TestAvailableMemory(t *testing.T) {
	b, ok := Available()
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return
	}
	if !ok || b < 1<<20 {
		t.Fatalf("available memory %d, %v", b, ok)
	}
}

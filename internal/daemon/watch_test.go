package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockContention(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	a, e := Acquire(p)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Release()
	if _, e = Acquire(p); e == nil {
		t.Fatal("second lock acquired")
	}
}
func TestStopRejectsBadPID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	if e := os.WriteFile(p, []byte("not-a-pid"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := Stop(p); e == nil {
		t.Fatal("bad pid accepted")
	}
}

package instance

import (
	"errors"
	"testing"
)

func TestAcquireExcludesSecondProcess(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := Acquire(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire() error = %v, want ErrLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire() after release: %v", err)
	}
	second.Close()
}

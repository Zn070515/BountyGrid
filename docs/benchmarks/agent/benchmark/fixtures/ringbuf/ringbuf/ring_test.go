package ringbuf

import "testing"

func TestNewRejectsNonPositiveSize(t *testing.T) {
	if _, err := New(0); err == nil {
		t.Fatal("New(0) returned no error")
	}
	if _, err := New(-1); err == nil {
		t.Fatal("New(-1) returned no error")
	}
}

func TestFIFO(t *testing.T) {
	r, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Push(1); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(2); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Pop(); !ok || got != 1 {
		t.Fatalf("Pop() = %d, %v; want 1, true", got, ok)
	}
	if got, ok := r.Pop(); !ok || got != 2 {
		t.Fatalf("Pop() = %d, %v; want 2, true", got, ok)
	}
	if _, ok := r.Pop(); ok {
		t.Fatal("Pop() on an empty ring returned ok")
	}
}

func TestOverwritesOldest(t *testing.T) {
	r, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Push(1)
	_ = r.Push(2)
	_ = r.Push(3)
	if got := r.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	if got, ok := r.Pop(); !ok || got != 2 {
		t.Fatalf("Pop() = %d, %v; want 2, true", got, ok)
	}
	if got, ok := r.Pop(); !ok || got != 3 {
		t.Fatalf("Pop() = %d, %v; want 3, true", got, ok)
	}
}

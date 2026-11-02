package counter

import (
	"sync"
	"testing"
)

func TestConcurrentAdd(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := c.Value(); got != 5000 {
		t.Fatalf("Value() = %d, want 5000", got)
	}
}

func TestConcurrentAddAndRead(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Add(1)
				_ = c.Value()
			}
		}()
	}
	wg.Wait()
	if got := c.Value(); got != 1000 {
		t.Fatalf("Value() = %d, want 1000", got)
	}
}

package counter

import "sync"

// Counter accumulates integer deltas.
type Counter struct {
	mu sync.Mutex
	n  int
}

// Add applies delta to the counter.
func (c *Counter) Add(delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n += delta
}

// Value returns the current value.
func (c *Counter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

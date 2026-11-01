package counter

// Counter accumulates integer deltas.
type Counter struct {
	n int
}

// Add applies delta to the counter.
func (c *Counter) Add(delta int) {
	c.n += delta
}

// Value returns the current value.
func (c *Counter) Value() int {
	return c.n
}

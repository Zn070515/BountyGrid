package ringbuf

// Ring is a fixed-size FIFO. When it is full, Push overwrites the oldest value.
type Ring struct {
	size int
}

// New returns a Ring that holds at most size values. size must be positive.
func New(size int) (*Ring, error) {
	return nil, nil
}

// Push adds v, overwriting the oldest value when the ring is full.
func (r *Ring) Push(v int) error {
	return nil
}

// Pop removes and returns the oldest value.
func (r *Ring) Pop() (int, bool) {
	return 0, false
}

// Len returns the number of stored values.
func (r *Ring) Len() int {
	return 0
}

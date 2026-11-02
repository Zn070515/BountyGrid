package ringbuf

import "errors"

// Ring is a fixed-size FIFO. When it is full, Push overwrites the oldest value.
type Ring struct {
	buf  []int
	head int
	tail int
	n    int
}

// New returns a Ring that holds at most size values. size must be positive.
func New(size int) (*Ring, error) {
	if size <= 0 {
		return nil, errors.New("ringbuf: size must be positive")
	}
	return &Ring{buf: make([]int, size)}, nil
}

// Push adds v, overwriting the oldest value when the ring is full.
func (r *Ring) Push(v int) error {
	if r.n == len(r.buf) {
		r.buf[r.tail] = v
		r.tail = (r.tail + 1) % len(r.buf)
		r.head = (r.head + 1) % len(r.buf)
		return nil
	}
	r.buf[r.tail] = v
	r.tail = (r.tail + 1) % len(r.buf)
	r.n++
	return nil
}

// Pop removes and returns the oldest value.
func (r *Ring) Pop() (int, bool) {
	if r.n == 0 {
		return 0, false
	}
	v := r.buf[r.head]
	r.head = (r.head + 1) % len(r.buf)
	r.n--
	return v, true
}

// Len returns the number of stored values.
func (r *Ring) Len() int { return r.n }

package process

import "sync"

// DefaultStderrRingCapacity is 64 KiB per vendor-runtime §8 and §13.
const DefaultStderrRingCapacity = 64 * 1024

// RingBuffer retains the most recent bytes written up to its capacity in a thread-safe circular buffer.
type RingBuffer struct {
	mu    sync.Mutex
	buf   []byte
	cap   int
	head  int
	count int
}

// NewRingBuffer allocates a ring buffer with the given capacity.
// If capacity <= 0, DefaultStderrRingCapacity is used.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = DefaultStderrRingCapacity
	}
	return &RingBuffer{
		buf: make([]byte, capacity),
		cap: capacity,
	}
}

// Write appends bytes to the ring buffer, evicting the oldest bytes when full.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	if n == 0 {
		return 0, nil
	}

	if n >= r.cap {
		copy(r.buf, p[n-r.cap:])
		r.head = 0
		r.count = r.cap
		return n, nil
	}

	first := r.cap - r.head
	if n > first {
		copy(r.buf[r.head:], p[:first])
		copy(r.buf[:n-first], p[first:])
		r.head = n - first
	} else {
		copy(r.buf[r.head:], p)
		r.head = (r.head + n) % r.cap
	}

	r.count += n
	if r.count > r.cap {
		r.count = r.cap
	}

	return n, nil
}

// Tail returns a copy of the recorded bytes in chronological order.
func (r *RingBuffer) Tail() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count == 0 {
		return nil
	}

	out := make([]byte, r.count)
	if r.count < r.cap {
		copy(out, r.buf[:r.count])
		return out
	}

	// Buffer is saturated: oldest bytes start at head.
	copy(out, r.buf[r.head:])
	copy(out[r.cap-r.head:], r.buf[:r.head])
	return out
}

// Len returns the current number of bytes retained.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

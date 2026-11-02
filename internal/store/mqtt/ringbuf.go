package mqtt

import (
	"sync"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

// logRingBuffer is a thread-safe circular buffer holding a fixed capacity of recent log entries.
type logRingBuffer struct {
	mu       sync.RWMutex
	capacity int
	entries  []model.LogEntry
	head     int // next write position
	size     int // count of stored items (<= capacity)
}

// newLogRingBuffer creates a ring buffer with the given capacity.
// If capacity <= 0, a default of 500 is used.
func newLogRingBuffer(capacity int) *logRingBuffer {
	if capacity <= 0 {
		capacity = 500
	}
	return &logRingBuffer{
		capacity: capacity,
		entries:  make([]model.LogEntry, capacity),
	}
}

// Append adds a new log entry to the buffer, overwriting the oldest entry when full.
func (rb *logRingBuffer) Append(entry model.LogEntry) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.entries[rb.head] = entry
	rb.head = (rb.head + 1) % rb.capacity
	if rb.size < rb.capacity {
		rb.size++
	}
}

// Slice returns up to `limit` most recent entries in chronological order.
// If limit <= 0 or limit >= size, all stored entries are returned.
func (rb *logRingBuffer) Slice(limit int) []model.LogEntry {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.size == 0 {
		return []model.LogEntry{}
	}

	count := rb.size
	if limit > 0 && limit < count {
		count = limit
	}

	// Calculate starting index for the slice.
	// The oldest valid entry is at (rb.head - rb.size + 2*rb.capacity) % rb.capacity.
	// The `count` most recent entries begin `size - count` items after the oldest entry.
	startOffset := rb.size - count
	startIdx := (rb.head - rb.size + 2*rb.capacity) % rb.capacity

	result := make([]model.LogEntry, count)
	for i := 0; i < count; i++ {
		idx := (startIdx + startOffset + i) % rb.capacity
		result[i] = rb.entries[idx]
	}
	return result
}

// Size returns the current number of log entries in the buffer.
func (rb *logRingBuffer) Size() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.size
}

// Clear resets the buffer.
func (rb *logRingBuffer) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.head = 0
	rb.size = 0
	rb.entries = make([]model.LogEntry, rb.capacity)
}

package execlog

import (
	"strings"
	"sync"
)

const TailBytes = 64 << 10

type Tail struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *Tail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n >= TailBytes {
		b.truncated = b.truncated || len(b.data)+n > TailBytes
		b.data = append(b.data[:0], p[n-TailBytes:]...)
		return n, nil
	}
	if discarded := len(b.data) + n - TailBytes; discarded > 0 {
		b.truncated = true
		b.data = b.data[:copy(b.data, b.data[discarded:])]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *Tail) Entry(execution string) Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := strings.ToValidUTF8(string(b.data), "")
	return Entry{Execution: execution, Text: text, Truncated: b.truncated || len(text) != len(b.data)}
}

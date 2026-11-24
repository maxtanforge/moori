package release

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
)

// Validation pipelines compile the same bytes repeatedly: wire verification
// and local execution re-verify one snapshot, admission replays stored
// releases, and maintenance flows compile the document Verify just compiled.
// The parse-normalize-digest pass dominates each call, so these memos cache
// the deterministic paths — Compile with no overrides, allowlist or resolver,
// and CompileExisting. Calls that carry overrides or a resolver stay uncached
// because their output depends on more than the input bytes.
//
// A cached Release is safe to share: its fields are unexported strings and a
// bool, exposed only through copying accessors. Context cancellation bypasses
// the cache in both directions so a canceled call can never poison later
// lookups with a spurious error.

type memoKey [sha256.Size]byte

type memoEntry struct {
	release Release
	err     error
}

// compileMemo caches compile results for byte-identical documents.
// Capacity-bounded with FIFO eviction.
type compileMemo struct {
	mu      sync.Mutex
	cap     int
	entries map[memoKey]memoEntry
	order   []memoKey
}

func newCompileMemo(capacity int) *compileMemo {
	return &compileMemo{cap: capacity, entries: make(map[memoKey]memoEntry, capacity)}
}

func (m *compileMemo) lookup(data []byte) (Release, error, bool) {
	key := sha256.Sum256(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[key]
	return entry.release, entry.err, ok
}

func (m *compileMemo) remember(data []byte, r Release, err error) {
	if isContextError(err) {
		return
	}
	key := sha256.Sum256(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[key]; !ok {
		if len(m.entries) >= m.cap {
			delete(m.entries, m.order[0])
			m.order = m.order[1:]
		}
		m.order = append(m.order, key)
	}
	m.entries[key] = memoEntry{release: r, err: err}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

var (
	compileCache  = newCompileMemo(64)
	existingCache = newCompileMemo(64)
)

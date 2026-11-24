package storage

import (
	"sync"
	"time"
)

const editConfigResultTTL = 30 * time.Second
const maxEditConfigResults = 8

type editConfigResult struct {
	payload   []byte
	expiresAt time.Time
	timer     *time.Timer
}

// Docker configuration being edited can contain credentials. Keep the narrow
// editor response out of the operation database, backups and audit payloads.
type editConfigResults struct {
	mu      sync.Mutex
	entries map[string]editConfigResult
}

func (r *editConfigResults) put(id string, payload []byte, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]editConfigResult)
	}
	if len(r.entries) >= maxEditConfigResults {
		var oldest string
		var expiresAt time.Time
		for key, entry := range r.entries {
			if oldest == "" || entry.expiresAt.Before(expiresAt) {
				oldest, expiresAt = key, entry.expiresAt
			}
		}
		r.entries[oldest].timer.Stop()
		delete(r.entries, oldest)
	}
	r.entries[id] = editConfigResult{
		payload:   payload,
		expiresAt: now.Add(editConfigResultTTL),
		timer:     time.AfterFunc(editConfigResultTTL, func() { r.remove(id) }),
	}
}

func (r *editConfigResults) get(id string, now time.Time) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[id]
	if !entry.expiresAt.After(now) {
		return nil
	}
	return entry.payload
}

func (r *editConfigResults) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries[id]; ok {
		entry.timer.Stop()
		delete(r.entries, id)
	}
}

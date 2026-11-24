// Package execlog stores bounded execution output separately from task state.
package execlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxFileBytes = 512 << 10
const maxFiles = 1024

var ErrConflict = errors.New("execution log has different content")
var ErrInvalid = errors.New("invalid execution log")

type Entry struct {
	Execution string `json:"execution"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

func (e Entry) valid() bool {
	return e.Execution != "" && len(e.Execution) <= 128 && len(e.Text) <= TailBytes && utf8.ValidString(e.Execution) && utf8.ValidString(e.Text)
}

type Store struct {
	dir    string
	budget int64
	mu     sync.Mutex
	// list mirrors the directory, oldest entry first, so Peek and makeRoom
	// never rescan it. It is rebuilt from disk when a read finds state the
	// mirror does not explain.
	list  []logFile
	bytes int64
}

func Open(dir string, budget int64) (*Store, error) {
	if dir == "" || budget <= 0 {
		return nil, ErrInvalid
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	s := &Store{dir: path, budget: budget}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.resyncLocked(); err != nil {
		return nil, err
	}
	if err := s.makeRoom(0, 0); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Put(entry Entry) error {
	if !entry.valid() {
		return ErrInvalid
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if len(data) > maxFileBytes || int64(len(data)) > s.budget {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(entry.Execution)
	previous, err := read(path)
	switch {
	case err == nil:
		if previous != entry {
			return ErrConflict
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := s.makeRoom(int64(len(data)), 1); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary.Name(), path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		s.trackLocked(logFile{path: path, size: info.Size(), modified: info.ModTime()})
	}
	return nil
}

func (s *Store) Read(execution string) (Entry, error) {
	if execution == "" || len(execution) > 128 {
		return Entry{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := read(s.path(execution))
	if err == nil && entry.Execution != execution {
		return Entry{}, ErrConflict
	}
	return entry, err
}

func (s *Store) Peek() (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.list) == 0 {
		return Entry{}, os.ErrNotExist
	}
	entry, err := read(s.list[0].path)
	if err != nil {
		// The mirror and the directory disagree; rebuild it once and retry so
		// files removed behind our back cannot wedge the head of the list.
		if resyncErr := s.resyncLocked(); resyncErr != nil {
			return Entry{}, resyncErr
		}
		if len(s.list) == 0 {
			return Entry{}, os.ErrNotExist
		}
		entry, err = read(s.list[0].path)
	}
	for errors.Is(err, ErrInvalid) || err == nil && s.path(entry.Execution) != s.list[0].path {
		path := s.list[0].path
		// Keep at most one quarantined copy, outside the spool index.
		if moveErr := os.Rename(path, filepath.Join(s.dir, ".corrupt-log")); moveErr != nil && !errors.Is(moveErr, os.ErrNotExist) {
			return Entry{}, moveErr
		}
		slog.Warn("isolated unreadable execution log", "file", filepath.Base(path))
		s.untrackLocked(path)
		if len(s.list) == 0 {
			return Entry{}, os.ErrNotExist
		}
		entry, err = read(s.list[0].path)
	}
	return entry, err
}

func (s *Store) Remove(execution string) error {
	if execution == "" || len(execution) > 128 {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(execution))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		s.untrackLocked(s.path(execution))
		return nil
	}
	return err
}

func (s *Store) path(execution string) string {
	digest := sha256.Sum256([]byte(execution))
	return filepath.Join(s.dir, hex.EncodeToString(digest[:])+".json")
}

func read(path string) (Entry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return Entry{}, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return Entry{}, err
	}
	var entry Entry
	if len(raw) > maxFileBytes || json.Unmarshal(raw, &entry) != nil || !entry.valid() {
		return Entry{}, ErrInvalid
	}
	return entry, nil
}

type logFile struct {
	path     string
	size     int64
	modified time.Time
}

func (s *Store) files() ([]logFile, error) {
	listing, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var result []logFile
	for _, file := range listing {
		digest, ok := strings.CutSuffix(file.Name(), ".json")
		if !ok || len(digest) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(digest); err != nil {
			continue
		}
		info, err := file.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		result = append(result, logFile{path: filepath.Join(s.dir, file.Name()), size: info.Size(), modified: info.ModTime()})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].modified.Equal(result[j].modified) {
			return result[i].path < result[j].path
		}
		return result[i].modified.Before(result[j].modified)
	})
	return result, nil
}

// makeRoom drops oldest entries until the incoming write fits the byte budget
// and the file cap, using the in-memory mirror instead of rescanning the
// directory.
func (s *Store) makeRoom(need int64, extra int) error {
	bytes := s.bytes + need
	count := len(s.list) + extra
	for len(s.list) > 0 && (bytes > s.budget || count > maxFiles) {
		file := s.list[0]
		if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.list = s.list[1:]
		s.bytes -= file.size
		bytes -= file.size
		count--
	}
	return nil
}

// resyncLocked rebuilds the directory mirror from disk.
func (s *Store) resyncLocked() error {
	files, err := s.files()
	if err != nil {
		return err
	}
	var total int64
	for _, file := range files {
		total += file.size
	}
	s.list = files
	s.bytes = total
	return nil
}

// trackLocked records a newly linked file in the mirror.
func (s *Store) trackLocked(file logFile) {
	s.untrackLocked(file.path)
	s.list = append(s.list, file)
	s.bytes += file.size
	sort.Slice(s.list, func(i, j int) bool {
		if s.list[i].modified.Equal(s.list[j].modified) {
			return s.list[i].path < s.list[j].path
		}
		return s.list[i].modified.Before(s.list[j].modified)
	})
}

// untrackLocked drops a path from the mirror if it is tracked.
func (s *Store) untrackLocked(path string) {
	for i, file := range s.list {
		if file.path != path {
			continue
		}
		s.bytes -= file.size
		s.list = append(s.list[:i], s.list[i+1:]...)
		return
	}
}

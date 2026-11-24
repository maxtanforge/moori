// Package composefiles manages explicitly registered Compose source files.
package composefiles

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxFiles       = 8
	maxFileBytes   = 64 << 10
	maxResultBytes = 480 << 10
)

var (
	ErrSourceUnavailable = errors.New("此 Compose 项目尚未注册源文件")
	ErrConflict          = errors.New("Compose 源文件已变化，请重新读取")
	ErrInvalidSource     = errors.New("已注册的 Compose 源文件无效或超过大小限制")
	projectName          = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
	sourceMu             sync.Mutex
)

type File struct {
	Path    string
	Content string
}

type Snapshot struct {
	Files    []File
	Revision string
}

type Sources struct {
	root     string
	rootInfo fs.FileInfo
	project  string
	paths    []string
}

// Open resolves a project exclusively through the operator's source manifest.
func Open(root, manifest, name string) (*Sources, error) {
	if root == "" || manifest == "" || !projectName.MatchString(name) {
		return nil, ErrSourceUnavailable
	}
	if !filepath.IsAbs(root) || len(manifest) > 128<<10 {
		return nil, ErrInvalidSource
	}
	var projects map[string][]string
	if json.Unmarshal([]byte(manifest), &projects) != nil {
		return nil, ErrInvalidSource
	}
	paths := projects[name]
	return openPaths(root, name, paths)
}

// OpenDiscovered resolves Compose's ordered absolute file paths within the
// operator-mounted source root.
func OpenDiscovered(root, name string, paths []string) (*Sources, error) {
	if root == "" || !projectName.MatchString(name) {
		return nil, ErrSourceUnavailable
	}
	if !filepath.IsAbs(root) || len(paths) > maxFiles {
		return nil, ErrInvalidSource
	}
	relative := make([]string, 0, len(paths))
	for _, file := range paths {
		if !filepath.IsAbs(file) || filepath.Clean(file) != file {
			return nil, ErrInvalidSource
		}
		rel, err := filepath.Rel(root, file)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, ErrInvalidSource
		}
		relative = append(relative, filepath.ToSlash(rel))
	}
	return openPaths(root, name, relative)
}

func openPaths(root, name string, paths []string) (*Sources, error) {
	if len(paths) == 0 {
		return nil, ErrSourceUnavailable
	}
	if len(paths) > maxFiles {
		return nil, ErrInvalidSource
	}
	seen := make(map[string]bool, len(paths))
	for _, name := range paths {
		local, err := filepath.Localize(name)
		ext := path.Ext(name)
		if err != nil || len(name) > 512 || strings.ContainsAny(name, "\\:\x00\r\n") ||
			ext != ".yaml" && ext != ".yml" || seen[local] {
			return nil, ErrInvalidSource
		}
		seen[local] = true
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, ErrSourceUnavailable
	}
	return &Sources{root: root, rootInfo: info, project: name, paths: paths}, nil
}

// Read returns the original file contents and ordered group revision.
func (s *Sources) Read() (Snapshot, error) {
	sourceMu.Lock()
	defer sourceMu.Unlock()
	root, err := s.openRoot()
	if err != nil {
		return Snapshot{}, err
	}
	defer root.Close()
	snapshot, _, err := s.read(root)
	return snapshot, err
}

func (s *Sources) openRoot() (*os.Root, error) {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, ErrSourceUnavailable
	}
	info, err := root.Stat(".")
	if err != nil || !os.SameFile(info, s.rootInfo) {
		root.Close()
		return nil, ErrSourceUnavailable
	}
	return root, nil
}

func (s *Sources) read(root *os.Root) (Snapshot, []fs.FileInfo, error) {
	snapshot := Snapshot{Files: make([]File, 0, len(s.paths))}
	infos := make([]fs.FileInfo, 0, len(s.paths))
	for _, name := range s.paths {
		dir, base, err := openParent(root, name)
		if err != nil {
			return Snapshot{}, nil, err
		}
		content, info, err := readRegular(dir, base)
		dir.Close()
		if err != nil {
			return Snapshot{}, nil, err
		}
		snapshot.Files = append(snapshot.Files, File{Path: name, Content: content})
		infos = append(infos, info)
	}
	if err := sealSnapshot(&snapshot); err != nil {
		return Snapshot{}, nil, err
	}
	return snapshot, infos, nil
}

func openParent(root *os.Root, name string) (*os.Root, string, error) {
	parts := strings.Split(name, "/")
	dir, err := root.OpenRoot(".")
	if err != nil {
		return nil, "", ErrInvalidSource
	}
	for _, part := range parts[:len(parts)-1] {
		info, err := dir.Lstat(part)
		if err != nil || !info.IsDir() {
			dir.Close()
			return nil, "", ErrInvalidSource
		}
		child, err := dir.OpenRoot(part)
		dir.Close()
		if err != nil {
			return nil, "", ErrInvalidSource
		}
		actual, err := child.Stat(".")
		if err != nil || !os.SameFile(info, actual) {
			child.Close()
			return nil, "", ErrInvalidSource
		}
		dir = child
	}
	return dir, parts[len(parts)-1], nil
}

func readRegular(dir *os.Root, name string) (string, fs.FileInfo, error) {
	before, err := dir.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxFileBytes {
		return "", nil, ErrInvalidSource
	}
	file, err := dir.OpenFile(name, readFlags, 0)
	if err != nil {
		return "", nil, ErrInvalidSource
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || !singleLink(info) {
		return "", nil, ErrInvalidSource
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", nil, ErrInvalidSource
	}
	return string(data), info, nil
}

func sealSnapshot(snapshot *Snapshot) error {
	h := sha256.New()
	for _, file := range snapshot.Files {
		for _, value := range []string{file.Path, file.Content} {
			h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
			io.WriteString(h, value)
		}
	}
	snapshot.Revision = hex.EncodeToString(h.Sum(nil))
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > maxResultBytes {
		return ErrInvalidSource
	}
	return nil
}

// Preview validates an in-memory edit and does not modify any source file.
func (s *Sources) Preview(revision, path, content string, validate func(before, after Snapshot) error) error {
	_, err := s.edit(revision, path, content, validate, false)
	return err
}

// Save atomically updates one registered file after validating the group.
func (s *Sources) Save(revision, path, content string) (Snapshot, error) {
	return s.SaveValidated(revision, path, content, nil)
}

// SaveValidated also runs the caller's full group check before committing.
func (s *Sources) SaveValidated(revision, path, content string, validate func(before, after Snapshot) error) (Snapshot, error) {
	return s.edit(revision, path, content, validate, true)
}

func (s *Sources) edit(revision, name, content string, validate func(before, after Snapshot) error, save bool) (Snapshot, error) {
	sourceMu.Lock()
	defer sourceMu.Unlock()
	root, err := s.openRoot()
	if err != nil {
		return Snapshot{}, err
	}
	defer root.Close()
	before, infos, err := s.read(root)
	if err != nil {
		return Snapshot{}, err
	}
	if before.Revision != revision {
		return Snapshot{}, ErrConflict
	}
	index := -1
	for i, file := range before.Files {
		if file.Path == name {
			index = i
			break
		}
	}
	if index < 0 {
		return Snapshot{}, ErrSourceUnavailable
	}
	if len(content) == 0 || len(content) > maxFileBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return Snapshot{}, ErrInvalidSource
	}
	after := Snapshot{Files: append([]File(nil), before.Files...)}
	after.Files[index].Content = content
	if err := sealSnapshot(&after); err != nil {
		return Snapshot{}, err
	}
	if err := validateDocuments(after); err != nil {
		return Snapshot{}, err
	}
	if validate != nil {
		if err := validate(before, after); err != nil {
			return Snapshot{}, err
		}
	}
	if !save || after.Revision == before.Revision {
		return after, nil
	}
	latest, current, err := s.read(root)
	if err != nil {
		return Snapshot{}, err
	}
	if latest.Revision != revision || !os.SameFile(infos[index], current[index]) {
		return Snapshot{}, ErrConflict
	}
	dir, base, err := openParent(root, name)
	if err != nil {
		return Snapshot{}, err
	}
	defer dir.Close()
	verify := func() error {
		latest, _, err := s.read(root)
		if err != nil {
			return err
		}
		if latest.Revision != revision {
			return ErrConflict
		}
		return nil
	}
	if err := replaceFile(dir, base, content, current[index], verify); err != nil {
		return Snapshot{}, err
	}
	return after, nil
}

func replaceFile(dir *os.Root, name, content string, info fs.FileInfo, verify func() error) error {
	temporary := ".moori-" + rand.Text()
	file, err := dir.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("无法在 Compose 源文件目录创建临时文件")
	}
	defer dir.Remove(temporary)
	defer file.Close()
	if _, err := io.WriteString(file, content); err != nil {
		return errors.New("无法写入 Compose 源文件")
	}
	if err := copyOwner(file, info); err != nil {
		return errors.New("无法保留 Compose 源文件所有者")
	}
	if err := copyAccessACL(dir, name, file, info); err != nil {
		return errors.New("无法保留 Compose 源文件访问权限")
	}
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		return errors.New("无法保留 Compose 源文件权限")
	}
	if err := file.Sync(); err != nil {
		return errors.New("无法同步 Compose 源文件")
	}
	if err := file.Close(); err != nil {
		return errors.New("无法关闭 Compose 临时文件")
	}
	// Check the group after the temporary file is fully prepared. Edits by
	// external writers cannot be locked across this check and the rename.
	if err := verify(); err != nil {
		return err
	}
	current, err := dir.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || current.Mode().Perm() != info.Mode().Perm() ||
		current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
		return ErrConflict
	}
	if err := dir.Rename(temporary, name); err != nil {
		return fmt.Errorf("无法替换 Compose 源文件: %w", ErrInvalidSource)
	}
	return nil
}

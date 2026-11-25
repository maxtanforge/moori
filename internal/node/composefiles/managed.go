package composefiles

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const managedSource = "compose.yaml"

var generationName = regexp.MustCompile(`^[A-Z2-7]{26}$`)

// OpenManaged resolves only the project directory recorded by Compose under the
// node-owned store. A new generation keeps archived files and bind data intact.
func OpenManaged(root, project, workingDir string) (*Sources, error) {
	if root == "" || !filepath.IsAbs(root) || !projectName.MatchString(project) ||
		filepath.Clean(workingDir) != workingDir || filepath.Dir(workingDir) != filepath.Clean(root) {
		return nil, ErrSourceUnavailable
	}
	generation, ok := strings.CutPrefix(filepath.Base(workingDir), project+".")
	if !ok || !generationName.MatchString(generation) {
		return nil, ErrSourceUnavailable
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, ErrInvalidSource
	}
	sources, err := openPaths(workingDir, project, []string{managedSource})
	if err != nil {
		return nil, ErrInvalidSource
	}
	sources.managed = true
	return sources, nil
}

// CreateManaged writes the original bytes in a fresh, private directory. A
// validation failure releases only the source and the newly allocated directory.
func CreateManaged(root, project, content string, validate func(*Sources, Snapshot) error) (*Sources, Snapshot, error) {
	if !filepath.IsAbs(root) || !projectName.MatchString(project) || !filepath.IsLocal(project) ||
		len(content) == 0 || len(content) > maxFileBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return nil, Snapshot{}, ErrInvalidSource
	}
	snapshot := Snapshot{Files: []File{{Path: managedSource, Content: content}}}
	if err := sealSnapshot(&snapshot); err != nil {
		return nil, Snapshot{}, err
	}
	if err := validateDocuments(snapshot); err != nil {
		return nil, Snapshot{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, Snapshot{}, errors.New("无法创建 Compose 项目存储目录")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, Snapshot{}, ErrInvalidSource
	}
	parent, err := os.OpenRoot(root)
	if err != nil {
		return nil, Snapshot{}, ErrInvalidSource
	}
	defer parent.Close()
	dir := project + "." + rand.Text()
	if err := parent.Mkdir(dir, 0700); err != nil {
		return nil, Snapshot{}, errors.New("无法创建 Compose 项目目录")
	}
	keep := false
	defer func() {
		if !keep {
			_ = parent.Remove(dir)
		}
	}()
	child, err := parent.OpenRoot(dir)
	if err != nil {
		return nil, Snapshot{}, ErrInvalidSource
	}
	defer child.Close()
	file, err := child.OpenFile(managedSource, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, Snapshot{}, errors.New("无法创建 Compose 源文件")
	}
	defer func() {
		if !keep {
			_ = child.Remove(managedSource)
		}
	}()
	_, err = io.WriteString(file, content)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err != nil || closeErr != nil {
		return nil, Snapshot{}, errors.New("无法保存 Compose 源文件")
	}
	sources, err := OpenManaged(root, project, filepath.Join(root, dir))
	if err != nil {
		return nil, Snapshot{}, err
	}
	if err := validate(sources, snapshot); err != nil {
		return nil, Snapshot{}, err
	}
	keep = true
	return sources, snapshot, nil
}

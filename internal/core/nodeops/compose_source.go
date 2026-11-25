package nodeops

import (
	"io/fs"
	"path"
	"strings"
)

const (
	ComposeSourceCreate  Action = "compose.source-create"
	ComposeSourceRead    Action = "compose.source-read"
	ComposeSourcePreview Action = "compose.source-preview"
	ComposeSourceSave    Action = "compose.source-save"
	ComposeSourceDeploy  Action = "compose.source-deploy"
)

type ComposeSourceEdit struct {
	Revision string `json:"revision"`
	Path     string `json:"path"`
	Content  string `json:"content"`
}

type ComposeSourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ComposeSourceResult struct {
	Files    []ComposeSourceFile `json:"files,omitempty"`
	Revision string              `json:"revision,omitempty"`
	Before   []string            `json:"before,omitempty"`
	After    []string            `json:"after,omitempty"`
}

func (a Action) ComposeFileOperation() bool {
	switch a {
	case ComposeSourceCreate, ComposeSourceRead, ComposeSourcePreview, ComposeSourceSave, ComposeSourceDeploy:
		return true
	default:
		return false
	}
}

func (r Request) validateComposeFile() error {
	if !ValidExistingComposeName(r.Target) || r.Edit != nil ||
		r.ForceRecreate && r.Action != ComposeSourceDeploy || r.Container != "" || r.Name != "" ||
		r.Tail != 0 || r.RegistryID != "" || r.Project != "" || r.Image != "" ||
		r.ImageID != "" || r.ExpectedImageID != nil || r.Overwrite {
		return ErrInvalid
	}
	if r.Action == ComposeSourceRead && r.ComposeSource == nil {
		return nil
	}
	edit := r.ComposeSource
	if r.Action == ComposeSourceRead || edit == nil ||
		len(edit.Path) > 512 || !fs.ValidPath(edit.Path) || strings.ContainsAny(edit.Path, "\\:") ||
		len(edit.Content) == 0 || len(edit.Content) > MaxComposeBytes/4 {
		return ErrInvalid
	}
	if r.Action == ComposeSourceCreate {
		if edit.Path != "compose.yaml" || edit.Revision != "" || r.Timestamps || r.AllLogRuns {
			return ErrInvalid
		}
		return nil
	}
	if !fullID.MatchString(edit.Revision) {
		return ErrInvalid
	}
	if ext := path.Ext(edit.Path); ext != ".yaml" && ext != ".yml" {
		return ErrInvalid
	}
	return nil
}

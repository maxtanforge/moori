package management

import (
	"context"
	"errors"
	"os"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/node/composefiles"
)

// EnableComposeSources sets the trusted Docker executable used for validation
// and deployment. The node administrator authorizes a source root separately.
func (o *Operations) EnableComposeSources(dockerPath string) { o.sourceDocker = dockerPath }

func (o *Operations) runComposeSource(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	if err := req.Validate(); err != nil {
		return nodeops.Result{}, err
	}
	root, manifest := os.Getenv("MOORI_COMPOSE_SOURCE_ROOT"), os.Getenv("MOORI_COMPOSE_SOURCES")
	sources, err := o.openComposeSources(ctx, root, manifest, req.Target)
	if err != nil {
		return nodeops.Result{}, err
	}
	if req.Action == nodeops.ComposeSourceRead {
		snapshot, err := sources.Read()
		if err != nil {
			return nodeops.Result{}, err
		}
		result := &nodeops.ComposeSourceResult{Revision: snapshot.Revision}
		for _, file := range snapshot.Files {
			result.Files = append(result.Files, nodeops.ComposeSourceFile{Path: file.Path, Content: file.Content})
		}
		return nodeops.Result{ComposeSource: result}, nil
	}
	if o.sourceDocker == "" {
		return nodeops.Result{}, errors.New("Docker Compose validation is unavailable")
	}
	edit := req.ComposeSource
	result := &nodeops.ComposeSourceResult{Revision: edit.Revision}
	validate := func(before, after composefiles.Snapshot) error {
		var err error
		if req.Action == nodeops.ComposeSourcePreview {
			// A broken version must still be repairable from the editor.
			result.Before, _ = sources.Services(ctx, o.sourceDocker, before)
		}
		result.After, err = sources.Services(ctx, o.sourceDocker, after)
		return err
	}
	if req.Action == nodeops.ComposeSourcePreview {
		if err := sources.Preview(edit.Revision, edit.Path, edit.Content, validate); err != nil {
			return nodeops.Result{}, err
		}
		return nodeops.Result{ComposeSource: result}, nil
	}
	saved, err := sources.SaveValidated(edit.Revision, edit.Path, edit.Content, validate)
	if err != nil {
		return nodeops.Result{}, err
	}
	result.Revision = saved.Revision
	if req.Action == nodeops.ComposeSourceDeploy {
		if err := sources.Apply(ctx, o.sourceDocker, saved.Revision, req.ForceRecreate); err != nil {
			return nodeops.Result{}, errors.Join(errors.New("Compose file saved; deployment did not complete"), err)
		}
	}
	return nodeops.Result{ComposeSource: result}, nil
}

func (o *Operations) openComposeSources(ctx context.Context, root, manifest, project string) (*composefiles.Sources, error) {
	if root != "" && manifest == "" {
		paths, err := o.docker.ComposeConfigFiles(ctx, project)
		if err != nil {
			return nil, err
		}
		return composefiles.OpenDiscovered(root, project, paths)
	}
	sources, err := composefiles.Open(root, manifest, project)
	if err != nil {
		return nil, err
	}
	if err := o.docker.CheckExistingCompose(ctx, project); err != nil {
		return nil, err
	}
	return sources, nil
}

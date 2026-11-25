package management

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/node/composefiles"
	"github.com/maxtanforge/moori/internal/node/executor"
)

// EnableComposeSources configures the trusted Docker CLI and the directory for
// projects created on this node. External projects use the operator source root.
func (o *Operations) EnableComposeSources(dockerPath, managedRoot string) {
	o.sourceDocker = dockerPath
	o.managedRoot = managedRoot
}

func (o *Operations) runComposeSource(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	if err := req.Validate(); err != nil {
		return nodeops.Result{}, err
	}
	root, manifest := os.Getenv("MOORI_COMPOSE_SOURCE_ROOT"), os.Getenv("MOORI_COMPOSE_SOURCES")
	if req.Action == nodeops.ComposeSourceCreate {
		return o.createCompose(ctx, manifest, req)
	}
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
	if o.managedRoot != "" {
		rows, err := o.docker.ComposeContainers(ctx, project)
		if err != nil {
			return nil, err
		}
		const wd = "com.docker.compose.project.working_dir"
		for _, row := range rows {
			dir := row.Labels[wd]
			sources, err := composefiles.OpenManaged(o.managedRoot, project, dir)
			if errors.Is(err, composefiles.ErrSourceUnavailable) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, other := range rows {
				if other.Labels[wd] != dir {
					return nil, errors.New("项目容器记录的 Compose 工作目录不一致")
				}
			}
			return sources, nil
		}
	}
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

func (o *Operations) createCompose(ctx context.Context, manifest string, req nodeops.Request) (nodeops.Result, error) {
	if o.sourceDocker == "" || o.managedRoot == "" {
		return nodeops.Result{}, errors.New("节点未配置原生 Compose 项目存储或 Docker Compose")
	}
	if manifest != "" {
		projects, err := composefiles.SourceManifest(manifest)
		if err != nil {
			return nodeops.Result{}, err
		}
		if _, registered := projects[req.Target]; registered {
			return nodeops.Result{}, errors.New("项目名已登记 Compose 源文件，请选择其他名称")
		}
	}
	checkAvailable := func() error {
		rows, err := o.docker.ComposeContainers(ctx, req.Target)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			return errors.New("同名 Compose 项目已存在，请选择其他名称")
		}
		return nil
	}
	if err := checkAvailable(); err != nil {
		return nodeops.Result{}, err
	}
	var services []string
	sources, snapshot, err := composefiles.CreateManaged(o.managedRoot, req.Target, req.ComposeSource.Content, func(s *composefiles.Sources, snap composefiles.Snapshot) error {
		var err error
		services, err = s.Services(ctx, o.sourceDocker, snap)
		if err != nil {
			return err
		}
		if len(services) == 0 {
			return errors.New("Compose 项目没有可启动的服务")
		}
		return checkAvailable()
	})
	if err != nil {
		return nodeops.Result{}, err
	}
	if err := sources.Apply(ctx, o.sourceDocker, snapshot.Revision, false); err != nil {
		if errors.Is(err, executor.ErrOutcomeUnknown) {
			return nodeops.Result{}, errors.Join(errors.New("Compose 源文件已保存，请刷新项目列表核查启动结果"), nodeops.ErrOutcomeUnknown)
		}
		return nodeops.Result{}, err
	}
	return nodeops.Result{ComposeSource: &nodeops.ComposeSourceResult{Revision: snapshot.Revision, After: services}, ResourceObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

package management

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	dockerops "github.com/maxtanforge/moori/internal/node/nodeops"
)

// The test executable stands in for the trusted Docker CLI. The fake daemon
// below separately supplies the working directory label Compose would persist.
func TestMain(m *testing.M) {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--host" {
		switch {
		case slices.Contains(args, "config") && slices.Contains(args, "--no-path-resolution"):
			fmt.Print(`{"services":{"app":{}}}`)
			os.Exit(0)
		case slices.Contains(args, "up") && slices.Contains(args, "--wait"):
			os.Exit(0)
		}
		os.Exit(2)
	}
	os.Exit(m.Run())
}

type projectDocker struct {
	*externalComposeDocker
	dir string
}

func (d *projectDocker) ContainerList(context.Context, container.ListOptions) ([]container.Summary, error) {
	if d.dir == "" {
		return nil, nil
	}
	return []container.Summary{{ID: d.id, Labels: map[string]string{"com.docker.compose.project": "external-app", "com.docker.compose.project.working_dir": d.dir}}}, nil
}

func TestNativeComposeActionsPreserveSourceAndRecreate(t *testing.T) {
	t.Setenv("MOORI_COMPOSE_SOURCE_ROOT", "")
	t.Setenv("MOORI_COMPOSE_SOURCES", "")
	d := &projectDocker{externalComposeDocker: &externalComposeDocker{id: strings.Repeat("a", 64)}}
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ops := &Operations{docker: &dockerops.Runner{Docker: d}}
	ops.EnableComposeSources(binary, root)
	content := "# package comment\nservices:\n  app:\n    image: nginx\n"
	create := nodeops.Request{Action: nodeops.ComposeSourceCreate, Target: "external-app", ComposeSource: &nodeops.ComposeSourceEdit{Path: "compose.yaml", Content: content}}
	run := func(req nodeops.Request) nodeops.Result {
		t.Helper()
		res, err := ops.Run(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := run(create)
	if res.ComposeSource == nil || res.ComposeSource.Revision == "" {
		t.Fatalf("create: %+v", res)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("managed directories %v %v", entries, err)
	}
	d.dir = filepath.Join(root, entries[0].Name())
	if _, err := ops.Run(t.Context(), create); err == nil {
		t.Fatal("overwrote active project")
	}
	read := run(nodeops.Request{Action: nodeops.ComposeSourceRead, Target: create.Target}).ComposeSource
	if len(read.Files) != 1 || read.Files[0].Content != content || read.Revision != res.ComposeSource.Revision {
		t.Fatalf("read: %+v", read)
	}
	edit := nodeops.Request{Target: create.Target, ComposeSource: &nodeops.ComposeSourceEdit{Path: "compose.yaml", Content: content + "# edit\n", Revision: read.Revision}}
	for _, action := range []nodeops.Action{nodeops.ComposeSourcePreview, nodeops.ComposeSourceSave, nodeops.ComposeSourceDeploy} {
		edit.Action = action
		res = run(edit)
		edit.ComposeSource.Revision = res.ComposeSource.Revision
	}
	run(nodeops.Request{Action: nodeops.ComposeRemove, Target: create.Target})
	if d.removed != d.id {
		t.Fatal("no removal")
	}
	d.dir = ""
	run(create)
	archived, err := os.ReadFile(filepath.Join(root, entries[0].Name(), "compose.yaml"))
	if err != nil || string(archived) != edit.ComposeSource.Content {
		t.Fatalf("archive: %v %v", string(archived), err)
	}
	d.dir = t.TempDir()
	if _, err := ops.Run(t.Context(), nodeops.Request{Action: nodeops.ComposeSourceRead, Target: create.Target}); err == nil {
		t.Fatal("reused stale managed source for unrelated project")
	}
	d.dir = ""
	t.Setenv("MOORI_COMPOSE_SOURCES", `{"external-app":["existing.yaml"]}`)
	if _, err := ops.Run(t.Context(), create); err == nil || errors.Is(err, os.ErrExist) {
		t.Fatalf("reserved name: %v", err)
	}
}

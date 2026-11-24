package composefiles

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/maxtanforge/moori/internal/node/executor"
)

func TestComposeServicesUsesTheCompleteOrderedStagedGroup(t *testing.T) {
	root, sources := sourceFixture(t)
	snapshot, err := sources.Read()
	if err != nil {
		t.Fatal(err)
	}
	var staged []string
	services, err := sources.services(t.Context(), "test-docker-executable", snapshot, func(cmd *exec.Cmd) error {
		want := []string{"--host", "unix:///var/run/docker.sock", "compose", "--env-file", os.DevNull,
			"--project-directory", filepath.Join(root, "project"), "-p", "app"}
		if len(cmd.Args) != 16 || !slices.Equal(cmd.Args[1:10], want) ||
			cmd.Args[10] != "-f" || cmd.Args[12] != "-f" || !slices.Equal(cmd.Args[14:], []string{"config", "--services"}) {
			t.Fatalf("unexpected Compose arguments: %v", cmd.Args)
		}
		staged = []string{cmd.Args[11], cmd.Args[13]}
		for i, name := range staged {
			data, err := os.ReadFile(name)
			if err != nil || string(data) != snapshot.Files[i].Content {
				t.Fatalf("staged file %d has wrong contents: %v", i, err)
			}
		}
		_, err := io.WriteString(cmd.Stdout, "web\nworker\n")
		return err
	})
	if err != nil || !slices.Equal(services, []string{"web", "worker"}) {
		t.Fatalf("service preview = %v, %v", services, err)
	}
	if _, err := os.Stat(staged[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary source persisted after Compose finished")
	}
	_, err = sources.services(t.Context(), "test-docker-executable", snapshot, func(cmd *exec.Cmd) error {
		io.WriteString(cmd.Stderr, "credential text from Docker")
		return errors.New("credential text from failure")
	})
	if err == nil || strings.Contains(err.Error(), "credential text") {
		t.Fatalf("Compose error was not redacted: %v", err)
	}
}

func TestComposePreviewRejectsIndirectSourcesAndAmbientEnvironmentBeforeCallingDocker(t *testing.T) {
	_, sources := sourceFixture(t)
	initial, err := sources.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		"include: [not-approved.yaml]\n",
		"x-key: &key include\n*key: [not-approved.yaml]\n",
		"services:\n  web:\n    extends: {file: not-approved.yaml, service: web}\n",
		"services:\n  web:\n    env_file: not-approved.env\n",
		"secrets:\n  token:\n    file: not-approved.secret\n",
		"services:\n  web:\n    image: nginx:${TAG}\n",
		"services:\n  web:\n    image: nginx:$TAG\n",
		"services:\n  web:\n    environment: [TOKEN]\n",
		"services:\n  web:\n    environment: {TOKEN: null}\n",
		"secrets:\n  token:\n    environment: TOKEN\n",
		"x-env: &env {TOKEN: null}\nservices:\n  web:\n    environment: {<<: *env}\n",
	} {
		called := false
		err := sources.Preview(initial.Revision, initial.Files[0].Path, content, func(_, _ Snapshot) error {
			called = true
			return nil
		})
		if err == nil || called {
			t.Fatal("indirect source reached Docker validation")
		}
	}
}

func TestComposeApplyUsesExistingProjectAndReportsUncertainProcessFailure(t *testing.T) {
	_, sources := sourceFixture(t)
	snapshot, err := sources.Read()
	if err != nil {
		t.Fatal(err)
	}
	err = sources.apply(t.Context(), "test-docker-executable", snapshot, true, func(cmd *exec.Cmd) (bool, error) {
		want := []string{"up", "--detach", "--no-build", "--pull", "missing", "--wait", "--wait-timeout", "120", "--force-recreate"}
		if !slices.Equal(cmd.Args[14:], want) {
			t.Fatalf("unexpected apply arguments: %v", cmd.Args)
		}
		io.WriteString(cmd.Stderr, "credential text from Docker")
		return true, errors.New("credential text from failure")
	})
	if !errors.Is(err, executor.ErrOutcomeUnknown) || strings.Contains(err.Error(), "credential text") {
		t.Fatalf("unexpected error after Compose launched: %v", err)
	}
	if err := sources.Apply(t.Context(), "test-docker-executable", strings.Repeat("f", 64), false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale file group was applied: %v", err)
	}
}

func TestComposePreviewRejectsAlternativeScalarDecoders(t *testing.T) {
	_, sources := sourceFixture(t)
	initial, err := sources.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		"x-fixture: !!binary Zm9v\n",
		"x-fixture: !unknown plain\n",
		"!unknown x-fixture: plain\n",
	} {
		if err := sources.Preview(initial.Revision, initial.Files[0].Path, content, nil); err == nil {
			t.Fatal("unexpected YAML type decoder accepted")
		}
	}
	if err := sources.Preview(initial.Revision, initial.Files[0].Path,
		"services:\n  web:\n    command: echo $$HOME\n    environment: {TEST: !!str value}\n", nil); err != nil {
		t.Fatalf("explicit environment values or escaped dollars rejected: %v", err)
	}
}

package nodeops

import (
	"strings"
	"testing"
)

func TestRequestRequiresExactResourcesAndKnownAction(t *testing.T) {
	valid := []Request{
		{Action: ContainerLogs, Target: strings.Repeat("a", 64), Tail: 100},
		{Action: ContainerLogs, Target: strings.Repeat("a", 64), Tail: 1000},
		{Action: ContainerLogs, Target: strings.Repeat("a", 64), Tail: 100, Timestamps: true},
		{Action: ContainerLogs, Target: strings.Repeat("a", 64), Timestamps: true},
		{Action: NetworkConnect, Target: strings.Repeat("a", 64), Container: strings.Repeat("b", 64)},
		{Action: VolumeCreate, Name: "my_data"},
		{Action: RegistryPull, Image: "alpine:3.22"},
	}
	invalid := []Request{
		{Action: ContainerStart, Target: "same-name"},
		{Action: ContainerStop, Target: strings.Repeat("a", 12)},
		{Action: ContainerLogs, Target: strings.Repeat("a", 64), Tail: MaxLogLines + 1},
		{Action: ContainerLogs, Target: strings.Repeat("a", 64), Tail: 99},
		{Action: ContainerStart, Target: strings.Repeat("a", 64), Timestamps: true},
		{Action: NetworkConnect, Target: strings.Repeat("a", 64), Container: "name"},
		{Action: VolumeDelete, Target: "../data"},
		{Action: VolumeCreate, Name: "data", Target: "other"},
		{Action: RegistryPull, Image: "https://registry/image"},
		{Action: RegistryVerify, Image: "registry.example/app:latest"},
		{Action: "container.exec"},
	}
	for _, req := range valid {
		if err := req.Validate(); err != nil {
			t.Errorf("valid %+v: %v", req, err)
		}
	}
	for _, req := range invalid {
		if err := req.Validate(); err == nil {
			t.Errorf("accepted invalid %+v", req)
		}
	}
}

func TestComposeSourceCreateValidation(t *testing.T) {
	const action Action = "compose.source-create"
	edit := ComposeSourceEdit{Path: "compose.yaml", Content: "services: {}"}
	req := Request{Action: action, Target: "my_project-2", ComposeSource: &edit}
	if err := req.Validate(); err != nil {
		t.Fatalf("valid create: %v", err)
	}
	if !action.Mutates() || !action.ComposeFileOperation() || action.LocalDirect() {
		t.Fatal("invalid dispatch classification")
	}
	for _, target := range []string{"", "../escape", "Project", "-option", "moori-" + strings.Repeat("a", 32)} {
		bad := req
		bad.Target = target
		if bad.Validate() == nil {
			t.Errorf("accepted target %q", target)
		}
	}
	for _, invalid := range []ComposeSourceEdit{
		{Path: "compose.yaml"}, {Path: "other/compose.yaml", Content: edit.Content},
		{Path: "compose.yaml", Revision: strings.Repeat("a", 64), Content: edit.Content},
		{Path: "compose.yaml", Content: strings.Repeat("x", MaxComposeBytes/4+1)},
	} {
		bad := req
		bad.ComposeSource = &invalid
		if bad.Validate() == nil {
			t.Fatal("accepted invalid create draft")
		}
	}
	req.ForceRecreate = true
	if req.Validate() == nil {
		t.Fatal("accepted force recreate during creation")
	}
}

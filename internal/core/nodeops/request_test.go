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

package localinv

import (
	"testing"

	"github.com/docker/docker/client"
)

func TestInventoryUsesExecutionSocket(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:43210")
	docker, closeClient, err := Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	if host := docker.(*client.Client).DaemonHost(); host != "unix:///var/run/docker.sock" {
		t.Fatalf("inventory endpoint: %q", host)
	}
}

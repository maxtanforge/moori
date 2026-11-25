package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentAcceptsConnectionEnvironment(t *testing.T) {
	t.Setenv("MOORI_ENDPOINT", "ws://control.example.test/agent/v1/connect")
	t.Setenv("MOORI_AGENT_TOKEN", strings.Repeat("A", 43))
	missingDocker := filepath.Join(t.TempDir(), "missing-docker")
	err := run(t.Context(), []string{"-docker", missingDocker})
	if err == nil || !strings.Contains(err.Error(), missingDocker) {
		t.Fatal("did not reach Docker initialization after reading the connection environment")
	}
	if os.Getenv("MOORI_AGENT_TOKEN") != "" {
		t.Fatal("token would be inherited by the Agent's child processes")
	}
}

func TestAgentRejectsMultipleTokenSources(t *testing.T) {
	t.Setenv("MOORI_ENDPOINT", "ws://control.example.test/agent/v1/connect")
	t.Setenv("MOORI_AGENT_TOKEN", strings.Repeat("A", 43))
	err := run(t.Context(), []string{"-credential-file", "credential"})
	if err == nil || !strings.Contains(err.Error(), "choose either") {
		t.Fatal("ambiguous token sources accepted")
	}
}

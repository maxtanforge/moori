package web

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestMonitoringBrowser(t *testing.T) {
	browser := os.Getenv("MOORI_BROWSER_BIN")
	if browser == "" {
		t.Skip("set MOORI_BROWSER_BIN to a Chromium executable; Node.js is also required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/monitoring.test.mjs", browser)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("monitoring browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}

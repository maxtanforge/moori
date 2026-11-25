package executor

import (
	"context"
	"strings"
	"testing"
)

func TestExternalResourceChecksRejectNonLocalResources(t *testing.T) {
	const doc = `{"services":{"app":{"image":"unused"}},"volumes":{"data":{"name":"external-volume","external":true}},"networks":{"shared":{"name":"external-net","external":true}}}`
	responses := map[string]string{
		"volume":  `{"name":"external-volume","driver":"local","scope":"local","options":0}`,
		"network": `{"name":"external-net","driver":"bridge","scope":"local"}`,
	}
	inspect := func(_ context.Context, kind string, names []string) ([]byte, error) {
		if len(names) != 1 {
			t.Fatalf("unexpected inspected resources: %v", names)
		}
		return []byte(responses[kind]), nil
	}
	if err := checkExternalResources(context.Background(), doc, inspect); err != nil {
		t.Fatal(err)
	}
	responses["volume"] = strings.Replace(responses["volume"], `"options":0`, `"options":1`, 1)
	if err := checkExternalResources(context.Background(), doc, inspect); err == nil {
		t.Fatal("accepted a local volume with driver options")
	}
	responses["volume"] = strings.Replace(responses["volume"], `"options":1`, `"options":0`, 1)
	responses["network"] = strings.Replace(responses["network"], `"bridge"`, `"overlay"`, 1)
	if err := checkExternalResources(context.Background(), doc, inspect); err == nil {
		t.Fatal("accepted a non-bridge network")
	}
}

package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxtanforge/moori/internal/core/release"
)

func TestStageGrantsFilesOnlyToBoundServiceAndEnforcesQuota(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secret-root")
	view := release.Verified{Compose: `{"services":{"web":{"image":"test","volumes":["data:/data"],"networks":["shared"],"healthcheck":{"test":["CMD","true"]}},"other":{"image":"test"}},"volumes":{"data":{}},"networks":{"shared":{"name":"node-net","external":true}}}`,
		Secrets: map[string]string{"PASSWORD": "v1"}, SecretServices: map[string][]string{"web": {"PASSWORD"}}}
	data := map[string][]byte{"PASSWORD": []byte("private-value")}
	document, cleanup, err := Stage(root, int64(len(data["PASSWORD"])), view, data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(document, "private-value") {
		t.Fatal("secret entered the runtime Compose document")
	}
	for _, field := range []string{`"volumes":{"data":{}}`, `"volumes":["data:/data"]`, `"networks":{"shared":{"name":"node-net","external":true}}`, `"healthcheck":{"test":["CMD","true"]}`} {
		if !strings.Contains(document, field) {
			t.Errorf("staging dropped %s", field)
		}
	}
	var mounted composeMounts
	if err := json.Unmarshal([]byte(document), &mounted); err != nil {
		t.Fatal(err)
	}
	if string(mounted.Services["web"]["secrets"]) != `["PASSWORD"]` || mounted.Services["other"]["secrets"] != nil {
		t.Fatal("secret was granted to the wrong service")
	}
	path := mounted.Secrets["PASSWORD"].File
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "private-value" {
		t.Fatalf("file source unavailable: %v", err)
	}
	if _, _, err := Stage(root, int64(len(data["PASSWORD"])), view, data); err == nil {
		t.Fatal("quota did not bound retained files")
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unlaunched secret file was retained: %v", err)
	}
}

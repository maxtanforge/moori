package localinv

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/docker/docker/api/types/container"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMountedHostWithDockerOffline(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MOORI_HOST_ROOT", root)
	t.Setenv("MOORI_DATA_DIR", "")
	files := map[string]string{
		"etc/hostname": "node-08", "etc/os-release": "PRETTY_NAME=\"Example Linux\"",
		"proc/sys/kernel/osrelease": "6.8", "proc/cpuinfo": "processor : 0\nmodel name : Example CPU\nprocessor : 1",
		"proc/meminfo": "MemTotal: 32 kB\nMemAvailable: 20 kB", "proc/uptime": "45.8 67",
		"proc/1/net/dev":   "eth2: 100 0 0 0 0 0 0 0 250 0 0 0 0 0 0 0",
		"proc/stat":        "cpu 40 0 0 60 0 0 0 0 0 0",
		"proc/1/mountinfo": "23 20 254:99 / /data rw - ext4 /dev/fixture rw",
	}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inv := New(nil)
	inv.ttl = 0
	o := inv.Overview(context.Background())
	h := o.Host
	if o.Available || h == nil || h.Hostname != "node-08" || h.OS != "Example Linux" || h.CPUCount != 2 || h.MemoryTotalBytes != 32768 || h.UptimeSeconds != 45 || len(h.Interfaces) != 1 || h.Interfaces[0].TxBytes != 250 {
		t.Fatal(o, h)
	}
	if err := os.WriteFile(filepath.Join(root, "proc", "stat"), []byte("cpu 60 0 0 80 0 0 0 0 0 0"), 0600); err != nil {
		t.Fatal(err)
	}
	next := inv.Overview(context.Background())
	if next.Host.CPUPercent == nil || *next.Host.CPUPercent != 50 {
		t.Fatal(next.Host)
	}
	if runtime.GOOS == "linux" && (len(o.Filesystems) != 1 || !o.Filesystems[0].UsageUnavailable) {
		t.Fatal(o.Filesystems)
	}
}

func TestDockerFieldsAreAllowlisted(t *testing.T) {
	d := fixture()
	d.containers[0].Labels = map[string]string{"private": "do-not-publish", "com.docker.compose.project": "shop", "com.docker.compose.service": "web"}
	d.containers[0].Command = "do-not-publish"
	d.containers[0].Ports = []container.Port{{PrivatePort: 80, PublicPort: 8080, Type: "tcp"}}
	d.volumes.Volumes[0].Options = map[string]string{"password": "do-not-publish"}
	o := New(d).Overview(context.Background())
	b, _ := json.Marshal(o)
	if bytes.Contains(b, []byte("do-not-publish")) || len(o.ComposeProjects) != 1 || o.ComposeProjects[0].Services[0] != "web" || len(o.Containers[1].Ports) != 1 {
		t.Fatal(o)
	}
}

func TestHostWarningIdentifiesUnreadablePath(t *testing.T) {
	for _, missingRoot := range []bool{true, false} {
		root := t.TempDir()
		want := "proc/meminfo"
		if missingRoot {
			root = filepath.Join(root, "missing-host")
			want = "missing-host"
		}
		t.Setenv("MOORI_HOST_ROOT", root)
		o := New(nil).Overview(context.Background())
		if o.Host != nil || !strings.Contains(filepath.ToSlash(o.Warning), want) {
			t.Fatalf("missingRoot=%v: host=%v warning=%q", missingRoot, o.Host, o.Warning)
		}
	}
}

//go:build linux

package localinv

import (
	"os"
	"testing"
)

func TestParseMountsFiltersPseudoAndDedupesDevices(t *testing.T) {
	content := []byte(`
sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
tmpfs /dev/shm tmpfs rw,nosuid,nodev 0 0
overlay / overlay rw 0 0
/dev/nvme0n1p2 / ext4 rw,relatime 0 0
/dev/nvme0n1p2 /data ext4 rw,relatime 0 0
/dev/nvme0n1p2 /data/projects ext4 rw,relatime 0 0
/dev/sda1 /mnt/backup vfat rw,relatime 0 0
cgroup /sys/fs/cgroup cgroup2 rw,nosuid,nodev,noexec 0 0
`)
	kept := parseMounts(content)
	if len(kept) != 2 {
		t.Fatal(kept)
	}
	if kept[0].mountpoint != "/" || kept[0].fstype != "ext4" || kept[0].device != "/dev/nvme0n1p2" {
		t.Fatal(kept[0])
	}
	if kept[1].mountpoint != "/mnt/backup" || kept[1].fstype != "vfat" || kept[1].device != "/dev/sda1" {
		t.Fatal(kept[1])
	}
}

func TestReadFilesystemsOnLiveMounts(t *testing.T) {
	content, err := os.ReadFile(mountsPath)
	if err != nil {
		t.Skip("no /proc/self/mounts on this machine")
	}
	systems := readFilesystems(content)
	if len(systems) == 0 {
		t.Fatal("expected at least one real filesystem with usage")
	}
	for _, item := range systems {
		if item.TotalBytes == 0 || item.UsedPercent < 0 || item.UsedPercent > 100 {
			t.Fatal(item)
		}
	}
}

package hostmetrics

import (
	"strings"
	"testing"
)

func TestProcMetrics(t *testing.T) {
	previous, err := readCPU(strings.NewReader("cpu  20 0 10 70 0 0 0 0 5 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := readCPU(strings.NewReader("cpu  40 0 20 140 0 0 0 0 10 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if value := current.utilization(previous); value == nil || *value != 3000 {
		t.Fatalf("CPU delta: %v", value)
	}
	if current.utilization(current) != nil || previous.utilization(current) != nil {
		t.Fatal("invalid or reset CPU counters produced utilization")
	}
	total, available, err := readMemory(strings.NewReader("MemTotal: 1024 kB\nMemFree: 64 kB\nMemAvailable: 512 kB\n"))
	if err != nil || total != 1048576 || available != 524288 {
		t.Fatalf("memory: %d/%d %v", available, total, err)
	}
	if _, _, err := readMemory(strings.NewReader("MemTotal: 1024 kB\n")); err == nil {
		t.Fatal("missing memory data reported as zero")
	}
}

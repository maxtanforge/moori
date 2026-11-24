package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
)

func TestEvaluateHostsSkipsInactiveLocalExecutor(t *testing.T) {
	ctx := context.Background()
	control, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	metrics, err := observability.Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer metrics.Close()
	if err := control.EnsureLocalExecutor(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := control.IssueAgentCredential(ctx, "worker", time.Hour); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Minute)
	for _, node := range []string{storage.LocalExecutorID, "worker"} {
		if err := metrics.RecordContact(ctx, node, 1, old); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := observability.PolicyFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluateHosts(ctx, control, metrics, policy, false); err != nil {
		t.Fatal(err)
	}
	for node, count := range map[string]int{storage.LocalExecutorID: 0, "worker": 1} {
		events, err := metrics.AlertHistory(ctx, node, 1, 100)
		if err != nil || len(events) != count {
			t.Fatalf("%s: events=%+v err=%v", node, events, err)
		}
	}
}

// Console-managed thresholds and notification targets must reach the
// evaluation loop without a restart; the bootstrap policy applies meanwhile.
func TestResolveAlertPolicyAppliesConsoleSettings(t *testing.T) {
	ctx := context.Background()
	control, err := storage.Open(ctx, filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if _, err := control.CreateHumanUser(ctx, "admin", "admin test password"); err != nil {
		t.Fatal(err)
	}
	if err := control.SetPlatformAdmin(ctx, "admin", true); err != nil {
		t.Fatal(err)
	}
	session, err := control.LoginHuman(ctx, "admin", "admin test password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := observability.DefaultPolicy()
	policy, err := resolveAlertPolicy(ctx, control, bootstrap)
	if err != nil || policy != bootstrap {
		t.Fatalf("default resolution = %+v, err = %v", policy, err)
	}
	target, err := control.CreateAlertTarget(ctx, session, "https://hooks.example.com/alert")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.SetAlertSettings(ctx, session, storage.AlertSettings{
		CPUPercent: 95, MemoryPercent: 92, SustainedSeconds: 180,
	}); err != nil {
		t.Fatal(err)
	}
	policy, err = resolveAlertPolicy(ctx, control, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if policy.CPUPercent != 95 || policy.MemoryPercent != 92 || policy.SustainedFor != 3*time.Minute || !policy.Notify {
		t.Fatalf("console policy = %+v", policy)
	}
	if err := control.DeleteAlertTarget(ctx, session, target.ID); err != nil {
		t.Fatal(err)
	}
	if policy, err = resolveAlertPolicy(ctx, control, bootstrap); err != nil || policy.Notify {
		t.Fatalf("policy after removing the last target = %+v, err = %v", policy, err)
	}
	if policy.CPUPercent != 95 {
		t.Fatalf("threshold override lost: %+v", policy)
	}
}

func TestServeValidatesMonitoringBeforeStartingLocalExecutor(t *testing.T) {
	for _, setting := range []struct {
		key, value string
	}{
		{"MOORI_ALERT_CPU_PERCENT", "101"},
		{"MOORI_ALERT_WEBHOOK", "invalid destination"},
	} {
		t.Run(setting.key, func(t *testing.T) {
			t.Setenv("MOORI_ALERT_CPU_PERCENT", "")
			t.Setenv("MOORI_ALERT_MEMORY_PERCENT", "")
			t.Setenv("MOORI_ALERT_SUSTAINED_FOR", "")
			t.Setenv("MOORI_ALERT_WEBHOOK", "")
			t.Setenv(setting.key, setting.value)
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := serve(ctx, []string{
				"-addr", "127.0.0.1:0", "-db", filepath.Join(dir, "moori.db"),
				"-metrics-db", dir, // A directory forces the optional metrics storage open to fail.
				"-log-budget", "0", "-outbox-retention", "0",
				"-local-execution", "on", "-local-docker", filepath.Join(dir, "missing-docker"),
			})
			if err == nil || !strings.Contains(err.Error(), setting.key) {
				t.Fatalf("serve error = %v, want %s validation before local Docker startup", err, setting.key)
			}
		})
	}
}

// A negative output budget would make execlog.Open fail and silently drop
// every deployment log; serve must reject it like a negative retention.
func TestServeRejectsNegativeLogBudget(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := serve(ctx, []string{
		"-addr", "127.0.0.1:0", "-db", filepath.Join(dir, "moori.db"),
		"-log-budget", "-1",
	})
	if err == nil || !strings.Contains(err.Error(), "log-budget") {
		t.Fatalf("serve error = %v, want log-budget validation", err)
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/maxtanforge/moori/internal/control/alertnotify"
	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/platform/maintenance"
)

func agentHealth(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("agent-health", flag.ContinueOnError)
	db := f.String("db", defaultDB(), "existing control database")
	metrics := f.String("metrics-db", "", "existing metrics database (default: <db>.metrics)")
	agent := f.String("agent", "", "registered Agent ID")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *agent == "" {
		return errors.New("agent-health requires -agent")
	}
	if *metrics == "" {
		*metrics = *db + ".metrics"
	}
	for _, path := range []string{*db, *metrics} {
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	control, err := storage.Open(ctx, *db)
	if err != nil {
		return err
	}
	defer control.Close()
	generation, err := control.AgentGeneration(ctx, *agent)
	if err != nil {
		return err
	}
	store, err := observability.Open(ctx, *metrics)
	if err != nil {
		return err
	}
	defer store.Close()
	status, err := store.Health(ctx, *agent, generation, time.Now())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}

type monitoringConfig struct {
	envPolicy observability.Policy
}

func configureMonitoring() (monitoringConfig, error) {
	policy, err := observability.PolicyFromEnv(os.Getenv)
	if err != nil {
		return monitoringConfig{}, err
	}
	// The console database is authoritative now, but a malformed legacy
	// destination still fails startup before local Docker work begins.
	if webhook := strings.TrimSpace(os.Getenv("MOORI_ALERT_WEBHOOK")); webhook != "" {
		if err := storage.ValidateAlertTarget(webhook); err != nil {
			return monitoringConfig{}, fmt.Errorf("MOORI_ALERT_WEBHOOK %v", err)
		}
	}
	return monitoringConfig{envPolicy: policy}, nil
}

func (config monitoringConfig) start(ctx context.Context, control *storage.Store, metrics *observability.Store, localEnabled bool) func() {
	ctx, cancel := context.WithCancel(ctx)
	stopPruning := maintenance.Start(ctx, "history cleanup failed", time.Hour, func(ctx context.Context) error {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := metrics.PruneMetricHistory(bounded, time.Now()); err != nil {
			return err
		}
		return metrics.PruneAlertEvents(bounded, time.Now())
	})
	var workers sync.WaitGroup
	workers.Go(func() { runHealthEvaluation(ctx, control, metrics, config.envPolicy, localEnabled) })
	notifier := alertnotify.New(control.AlertNotificationTargets)
	notifier.NodeActive = func(ctx context.Context, agent string, generation int64) (bool, error) {
		if agent == storage.LocalExecutorID && !localEnabled {
			return false, nil
		}
		err := control.ValidateAgent(ctx, agent, generation)
		if errors.Is(err, storage.ErrConflict) {
			return false, nil
		}
		return err == nil, err
	}
	workers.Go(func() { notifier.Run(ctx, metrics) })
	return func() {
		cancel()
		stopPruning()
		workers.Wait()
	}
}

func runHealthEvaluation(ctx context.Context, control *storage.Store, metrics *observability.Store, bootstrap observability.Policy, localEnabled bool) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	policy := bootstrap
	// Give the in-process node one evaluation interval to record contact.
	includeLocal := false
	for ctx.Err() == nil {
		if resolved, err := resolveAlertPolicy(ctx, control, bootstrap); err != nil {
			slog.Warn("alert policy lookup failed", "err", err)
		} else {
			policy = resolved
		}
		if err := evaluateHosts(ctx, control, metrics, policy, includeLocal); err != nil && ctx.Err() == nil {
			slog.Warn("host incident evaluation failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			includeLocal = localEnabled
		}
	}
}

// resolveAlertPolicy picks up console-managed thresholds and notification
// targets between evaluations, so changes apply without a restart. The
// bootstrap policy applies until a console override exists.
func resolveAlertPolicy(ctx context.Context, control *storage.Store, bootstrap observability.Policy) (observability.Policy, error) {
	policy := bootstrap
	settings, ok, err := control.AlertSettings(ctx)
	if err != nil {
		return policy, err
	}
	if ok {
		policy.CPUPercent = settings.CPUPercent
		policy.MemoryPercent = settings.MemoryPercent
		policy.SustainedFor = time.Duration(settings.SustainedSeconds) * time.Second
	}
	targets, err := control.AlertNotificationTargets(ctx)
	if err != nil {
		return policy, err
	}
	policy.Notify = len(targets) > 0
	return policy, nil
}

func evaluateHosts(ctx context.Context, control *storage.Store, metrics *observability.Store, policy observability.Policy, includeLocal bool) error {
	var after string
	var firstError error
	for ctx.Err() == nil {
		page, err := control.ListAgentGenerations(ctx, after)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return firstError
		}
		for _, agent := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			after = agent.ID
			if agent.ID == storage.LocalExecutorID && !includeLocal {
				continue
			}
			if err := metrics.EvaluateWithPolicy(ctx, agent.ID, agent.Generation, time.Now(), policy); err != nil && firstError == nil {
				firstError = err
			}
		}
	}
	return ctx.Err()
}

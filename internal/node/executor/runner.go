// Package executor launches accepted and validated Compose deployments.
package executor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/core/release"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/secrets"
	"github.com/maxtanforge/moori/internal/platform/execlog"
)

var ErrOutcomeUnknown = errors.New("execution outcome requires reconciliation")
var ErrStoppedBeforeLaunch = errors.New("Docker process was not started")
var ErrLaunchFailed = errors.New("Docker process failed to start")

type Plan struct {
	Project, Compose string
	Timeout          time.Duration
	Output           io.Writer
	ImagesReady      bool
	ForceRecreate    bool
}

// Apply runs the deployment and health check. It returns ErrOutcomeUnknown if
// Docker state may still change. All other errors denote a known failure.
type Apply func(context.Context, Plan) error

type Runner struct {
	Ledger        *ledger.Ledger
	Apply         Apply
	Logs          *execlog.Store
	FetchSecrets  func(context.Context, string, string) (map[string][]byte, error)
	FetchRegistry registry.Source
	PullImage     func(context.Context, string, string, registry.Credential) error
	SecretRoot    string
	SecretBudget  int64
}

func (r Runner) RunAccepted(ctx context.Context, id, document string) error {
	if r.Ledger == nil || r.Apply == nil {
		return errors.New("executor is not configured")
	}
	entry, err := r.Ledger.Get(ctx, id)
	if err != nil {
		return err
	}
	if entry.State != deployment.Dispatching {
		return ledger.ErrState
	}
	input, err := release.Verify(ctx, document, entry.Digest)
	if err != nil {
		return err
	}
	if input.Target != entry.Target {
		return ledger.ErrConflict
	}
	var images []registry.Image
	var credentials map[string]registry.Credential
	prepare := r.PullImage != nil || r.FetchRegistry != nil
	plan := Plan{Project: release.ComposeProjectName(input.Target), Compose: input.Compose, Timeout: input.HealthTimeout, ImagesReady: prepare, ForceRecreate: input.ForceRecreate}
	if r.Logs != nil && len(input.Secrets) == 0 {
		var tail execlog.Tail
		plan.Output = &tail
		// Final state is persisted before this best-effort disk write.
		defer func() {
			entry := tail.Entry(id)
			if entry.Text == "" && !entry.Truncated {
				return
			}
			if err := r.Logs.Put(entry); err != nil {
				slog.Debug("deployment output could not be retained", "execution", id, "err", err)
			}
		}()
	}
	if prepare {
		if r.PullImage == nil || r.FetchRegistry == nil {
			return r.prepareFailure(ctx, id, errors.New("registry image preparation is not configured"))
		}
		images, err = registry.ComposeImages(input.Compose)
		if err != nil {
			return r.prepareFailure(ctx, id, err)
		}
		credentials, err = r.fetchRegistry(ctx, id, entry.Digest)
		defer clear(credentials)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if plan.Output != nil {
				_, _ = io.WriteString(plan.Output, err.Error()+"\n")
			}
			return r.prepareFailure(ctx, id, err)
		}
	}
	var bundle map[string][]byte
	if len(input.Secrets) != 0 {
		if r.FetchSecrets == nil || r.SecretRoot == "" || r.SecretBudget <= 0 {
			return errors.Join(errors.New("secret delivery is not configured"), r.Ledger.Record(ctx, id, deployment.Failed))
		}
		bundle, err = r.FetchSecrets(ctx, id, entry.Digest)
		defer func() {
			for _, value := range bundle {
				clear(value)
			}
		}()
		if err != nil {
			if errors.Is(err, secrets.ErrDenied) && ctx.Err() == nil {
				return errors.Join(err, r.Ledger.Record(ctx, id, deployment.Failed))
			}
			return err
		}
	}
	// The conditional state transition commits before external side effects.
	// A second goroutine or a restarted executor cannot pass this gate.
	if err := r.Ledger.Record(ctx, id, deployment.Executing); err != nil {
		return err
	}
	if prepare {
		pullCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		for _, image := range images {
			if err := r.PullImage(pullCtx, image.Reference, "", credentials[image.Reference]); err != nil {
				cancel()
				if ctx.Err() != nil {
					return r.recordStoppedBeforeLaunch(ctx, id)
				}
				err := errors.New("image pull failed for " + image.Reference)
				if plan.Output != nil {
					_, _ = io.WriteString(plan.Output, err.Error()+"\n")
				}
				return r.prepareFailure(ctx, id, err)
			}
		}
		cancel()
		clear(credentials)
		if ctx.Err() != nil {
			return r.recordStoppedBeforeLaunch(ctx, id)
		}
	}
	var releaseFiles func()
	if len(input.Secrets) != 0 {
		plan.Compose, releaseFiles, err = secrets.Stage(r.SecretRoot, r.SecretBudget, input, bundle)
		for _, value := range bundle {
			clear(value)
		}
		if err != nil {
			putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return errors.Join(err, r.Ledger.Record(putCtx, id, deployment.Failed))
		}
	}
	err = r.Apply(ctx, plan)
	if releaseFiles != nil && (errors.Is(err, ErrStoppedBeforeLaunch) || errors.Is(err, ErrLaunchFailed)) {
		releaseFiles()
	}
	if err != nil && plan.Output != nil {
		_, _ = io.WriteString(plan.Output, "\n"+err.Error()+"\n")
	}
	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if errors.Is(err, ErrStoppedBeforeLaunch) {
		return r.Ledger.Record(resultCtx, id, deployment.Canceled)
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		return errors.Join(err, r.Ledger.Record(resultCtx, id, deployment.Reconciling))
	}
	if err != nil {
		return errors.Join(err, r.Ledger.Record(resultCtx, id, deployment.Failed))
	}
	if err := r.Ledger.Record(resultCtx, id, deployment.Verifying); err != nil {
		return errors.Join(ErrOutcomeUnknown, err)
	}
	if err := r.Ledger.Record(resultCtx, id, deployment.Succeeded); err != nil {
		return errors.Join(ErrOutcomeUnknown, err)
	}
	return nil
}

func (r Runner) prepareFailure(ctx context.Context, id string, err error) error {
	putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return errors.Join(err, r.Ledger.Record(putCtx, id, deployment.Failed))
}

func (r Runner) recordStoppedBeforeLaunch(ctx context.Context, id string) error {
	putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return r.Ledger.Record(putCtx, id, deployment.Canceled)
}

func (r Runner) fetchRegistry(ctx context.Context, id, digest string) (map[string]registry.Credential, error) {
	const attempts = 3
	for attempt := 1; attempt <= attempts; attempt++ {
		credentials, err := r.FetchRegistry(ctx, id, digest)
		if err == nil {
			return credentials, nil
		}
		clear(credentials)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, registry.ErrAmbiguous):
			return nil, registry.ErrAmbiguous
		case errors.Is(err, registry.ErrDenied):
			return nil, registry.ErrDenied
		case errors.Is(err, registry.ErrSecureTransport):
			return nil, registry.ErrSecureTransport
		case attempt < attempts:
			timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, registry.ErrRetry
}

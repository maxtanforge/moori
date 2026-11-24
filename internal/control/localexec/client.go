// Package localexec connects the control plane directly to its local execution
// ledger. It reuses admission, cancellation and recovery without a network Agent.
package localexec

import (
	"context"
	"errors"
	"time"

	"github.com/maxtanforge/moori/internal/control/dispatch"
	"github.com/maxtanforge/moori/internal/control/observability"
	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
	"github.com/maxtanforge/moori/internal/core/telemetry"
	"github.com/maxtanforge/moori/internal/node/executor"
	"github.com/maxtanforge/moori/internal/node/ledger"
	"github.com/maxtanforge/moori/internal/node/localinv"
	"github.com/maxtanforge/moori/internal/node/secrets"
	"github.com/maxtanforge/moori/internal/node/session"
	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
	"github.com/maxtanforge/moori/internal/transport/imagearchive"
)

type Options struct {
	ImageArchives *imagearchive.Relay
	Store         *storage.Store
	Ledger        *ledger.Ledger
	Apply         executor.Apply
	Logs          *execlog.Store
	Spool         *execlog.Store
	Health        *observability.Store
	NodeState     *observability.Store
	Collect       func() (telemetry.Sample, error)
	Inventory     func(context.Context) localinv.Overview
	SecretKey     storage.SecretKey
	SecretRoot    string
	PullImage     func(context.Context, string, string, registry.Credential) error
	NodeOperation func(context.Context, nodeops.Request) (nodeops.Result, error)
}

func NewClient(o Options) session.Client {
	peer := agentwire.Peer{ID: storage.LocalExecutorID, Generation: 1}
	service := dispatch.Session{Store: o.Store, Logs: o.Logs, Health: o.Health, NodeState: o.NodeState, AbortArchive: o.ImageArchives.Abort, ValidatePeer: func(ctx context.Context, p agentwire.Peer) error {
		return o.Store.ValidateAgent(ctx, p.ID, p.Generation)
	}}
	runner := executor.Runner{Ledger: o.Ledger, Apply: o.Apply, Logs: o.Spool, SecretRoot: o.SecretRoot, SecretBudget: 64 << 20}
	if o.PullImage != nil {
		runner.PullImage = o.PullImage
		runner.FetchRegistry = func(ctx context.Context, id, digest string) (map[string]registry.Credential, error) {
			return o.Store.ExecutionRegistry(ctx, o.SecretKey, peer.ID, peer.Generation, id, digest, true)
		}
	}
	if o.SecretKey != (storage.SecretKey{}) {
		runner.FetchSecrets = func(ctx context.Context, id, digest string) (map[string][]byte, error) {
			values, err := o.Store.ExecutionSecrets(ctx, o.SecretKey, peer.ID, peer.Generation, id, digest)
			if errors.Is(err, storage.ErrForbidden) || errors.Is(err, storage.ErrSecretUnavailable) || errors.Is(err, storage.ErrSecretKey) {
				err = errors.Join(secrets.ErrDenied, err)
			}
			return values, err
		}
	}
	var lastContact time.Time
	return session.Client{Session: session.Session{Ledger: o.Ledger}, Execute: runner.RunAccepted, Logs: o.Spool, Collect: o.Collect, Inventory: o.Inventory, NodeOperation: o.NodeOperation,
		Direct: func(ctx context.Context, m agentwire.Message) (agentwire.Message, error) {
			reply, err := service.Reply(ctx, peer, m)
			if err == nil && time.Since(lastContact) >= 15*time.Second {
				lastContact = time.Now()
				service.RecordContact(ctx, peer, lastContact)
			}
			return reply, err
		},
	}
}

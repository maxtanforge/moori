package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/maxtanforge/moori/internal/core/deployment"
)

type Offer struct {
	ID, Execution, Target, Agent, Digest, Document string
	Generation                                     int64
	Expires                                        time.Time
	State                                          deployment.State
}

// Offer reloads the original identity and payload. Dispatchers must inspect
// State and reconcile uncertain executions before deciding to send anything.
func (s *Store) Offer(ctx context.Context, execution string) (Offer, error) {
	return scanOffer(s.db.QueryRowContext(ctx, offerQuery+` WHERE d.execution_id=? AND d.expires_ms IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM deployment_cancellations c WHERE c.deployment_id=d.id)
 AND NOT (d.state='dispatching' AND `+invalidExternalAssignment+`)
 AND `+canDispatchTarget+` AND d.dispatch_epoch=coalesce((SELECT epoch FROM recovery_state WHERE singleton=1),'')`, execution))
}

const offerQuery = `SELECT d.id,d.execution_id,d.target,d.agent,d.digest,p.document,
 d.generation,d.expires_ms,d.state FROM deployments d JOIN snapshots p ON p.deployment_id=d.id`

func scanOffer(row *sql.Row) (Offer, error) {
	var offer Offer
	var epoch int64
	err := row.Scan(
		&offer.ID, &offer.Execution, &offer.Target, &offer.Agent, &offer.Digest, &offer.Document,
		&offer.Generation, &epoch, &offer.State)
	if err != nil {
		return Offer{}, err
	}
	offer.Expires = time.UnixMilli(epoch).UTC()
	return offer, nil
}

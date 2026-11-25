package deployment

type CancelDecision string

const (
	CancelLocally      CancelDecision = "cancel_locally"
	RequestAgentCancel CancelDecision = "request_agent_cancel"
	ReconcileCancel    CancelDecision = "reconcile_cancel"
	CancelTooLate      CancelDecision = "too_late"
	CancelInvalidState CancelDecision = "invalid_state"
)

func CancellationDecision(state State) CancelDecision {
	switch state {
	case Queued:
		return CancelLocally
	case Dispatching, Executing, Verifying:
		return RequestAgentCancel
	case Reconciling, NeedsAttention:
		return ReconcileCancel
	case Succeeded, Failed, Canceled, Abandoned:
		return CancelTooLate
	default:
		return CancelInvalidState
	}
}

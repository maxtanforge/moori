package deployment

type State string

const (
	Queued         State = "queued"
	Dispatching    State = "dispatching"
	Executing      State = "executing"
	Verifying      State = "verifying"
	Reconciling    State = "reconciling"
	NeedsAttention State = "needs_attention"
	Succeeded      State = "succeeded"
	Failed         State = "failed"
	Canceled       State = "canceled"
	Abandoned      State = "abandoned"
)

func (s State) Terminal() bool {
	return s == Succeeded || s == Failed || s == Canceled || s == Abandoned
}

// CanTransition only checks topology. Callers must check authorization,
// execution evidence and compare-and-swap version in the same transaction.
// Duplicate events are acknowledged by event identity, not self-transitions.
func CanTransition(from, to State) bool {
	switch from {
	case Queued:
		return to == Dispatching || to == Canceled || to == Failed
	case Dispatching:
		return to == Executing || to == Reconciling || to == Failed || to == Canceled
	case Executing:
		return to == Verifying || to == Failed || to == Canceled || to == Reconciling
	case Verifying:
		return to == Succeeded || to == Failed || to == Canceled || to == Reconciling
	case Reconciling:
		return to == Executing || to == Verifying || to == Succeeded ||
			to == Failed || to == Canceled || to == NeedsAttention || to == Abandoned
	case NeedsAttention:
		return to == Reconciling || to == Abandoned
	default:
		return false
	}
}

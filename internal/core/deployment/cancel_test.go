package deployment

import "testing"

func TestCancellationDecision(t *testing.T) {
	cases := []struct {
		state State
		want  CancelDecision
	}{
		{Queued, CancelLocally},
		{Dispatching, RequestAgentCancel}, {Executing, RequestAgentCancel},
		{Verifying, RequestAgentCancel}, {Reconciling, ReconcileCancel},
		{NeedsAttention, ReconcileCancel}, {Succeeded, CancelTooLate},
		{Failed, CancelTooLate}, {Canceled, CancelTooLate},
		{State("invalid"), CancelInvalidState},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := CancellationDecision(tc.state); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLateCancellationCannotRewriteSuccess(t *testing.T) {
	if CancellationDecision(Succeeded) != CancelTooLate {
		t.Fatal("late cancellation must be rejected")
	}
	if CanTransition(Succeeded, Canceled) {
		t.Fatal("confirmed success must remain immutable")
	}
	if CanTransition(Reconciling, Queued) {
		t.Fatal("unknown execution must not be blindly retried")
	}
}

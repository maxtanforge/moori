package deployment

import "testing"

func TestTransitionMatrix(t *testing.T) {
	states := []State{Queued, Dispatching, Executing, Verifying,
		Reconciling, NeedsAttention, Succeeded, Failed, Canceled, Abandoned}
	allowed := map[State][]State{
		Queued:         {Dispatching, Canceled, Failed},
		Dispatching:    {Executing, Reconciling, Failed, Canceled},
		Executing:      {Verifying, Failed, Canceled, Reconciling},
		Verifying:      {Succeeded, Failed, Canceled, Reconciling},
		Reconciling:    {Executing, Verifying, Succeeded, Failed, Canceled, NeedsAttention, Abandoned},
		NeedsAttention: {Reconciling, Abandoned},
	}
	for _, from := range states {
		for _, to := range states {
			want := false
			for _, s := range allowed[from] {
				if s == to {
					want = true
				}
			}
			t.Run(string(from)+"/"+string(to), func(t *testing.T) {
				if got := CanTransition(from, to); got != want {
					t.Fatalf("CanTransition = %v, want %v", got, want)
				}
			})
		}
	}
	if CanTransition(State("invalid"), Queued) || CanTransition(Queued, State("invalid")) {
		t.Fatal("invalid state accepted")
	}
}

func TestTerminal(t *testing.T) {
	for _, s := range []State{Succeeded, Failed, Canceled, Abandoned} {
		if !s.Terminal() {
			t.Fatalf("%s should be terminal", s)
		}
	}
	for _, s := range []State{Queued, Dispatching, Executing,
		Verifying, Reconciling, NeedsAttention, State("invalid")} {
		if s.Terminal() {
			t.Fatalf("%s should not be terminal", s)
		}
	}
}

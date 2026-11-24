package deployment

// Evidence is the latest state retained for an execution in an Agent ledger.
type Evidence struct {
	Execution string `json:"execution"`
	Target    string `json:"target"`
	Digest    string `json:"digest"`
	Sequence  int64  `json:"sequence"`
	State     State  `json:"state"`
}

const InventoryPageSize = 32

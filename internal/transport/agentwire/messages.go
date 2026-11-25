package agentwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/core/nodeops"
)

const (
	KindPoll          = "execution.poll"
	KindIdle          = "execution.idle"
	KindHeartbeat     = "agent.heartbeat"
	KindOffer         = "execution.offer"
	KindReceipt       = "execution.receipt"
	KindReceiptAck    = "execution.receipt.ack"
	KindReport        = "execution.report"
	KindAck           = "event.ack"
	KindRecovery      = "recovery.begin"
	KindInventory     = "recovery.inventory"
	KindInventoryAck  = "recovery.inventory.ack"
	KindCancel        = "execution.cancel"
	KindCancelReply   = "execution.cancel.reply"
	KindCancelAck     = "execution.cancel.ack"
	KindOutput        = "execution.output"
	KindOutputAck     = "execution.output.ack"
	KindHostSample    = "host.sample"
	KindHostSampleAck = "host.sample.ack"
	// Node inventory: the Agent pushes a snapshot of its machine's Docker
	// resources and filesystem usage right after connecting and then
	// periodically. The overview is passed through opaquely so the transport
	// stays free of domain packages; the control side validates it.
	KindNodeInventory          = "node.inventory"
	KindNodeInventoryAck       = "node.inventory.ack"
	KindNodeOperationPoll      = "node.operation.poll"
	KindNodeOperationHeartbeat = "node.operation.heartbeat"
	KindNodeOperation          = "node.operation"
	KindNodeOperationResult    = "node.operation.result"
	KindNodeOperationResultAck = "node.operation.result.ack"
)

type OutputAck struct {
	Retry    bool   `json:"retry,omitempty"`
	ID       string `json:"id"`
	Retained bool   `json:"retained"`
}

type Cancel struct {
	Execution string `json:"execution"`
}

type CancelReply struct {
	Execution string `json:"execution"`
	Uncertain bool   `json:"uncertain"`
}

type Recovery struct {
	Epoch string `json:"epoch"`
}

type Inventory struct {
	Epoch    string                `json:"epoch"`
	Records  []deployment.Evidence `json:"records"`
	Complete bool                  `json:"complete"`
}

type Offer struct {
	Execution string          `json:"execution"`
	Target    string          `json:"target"`
	Digest    string          `json:"digest"`
	ExpiresMS int64           `json:"expires_ms"`
	Snapshot  json.RawMessage `json:"snapshot"`
}

type Receipt struct {
	Execution string           `json:"execution"`
	State     deployment.State `json:"state"`
}

type Report struct {
	ID        string           `json:"id"`
	Execution string           `json:"execution"`
	Sequence  int64            `json:"sequence"`
	State     deployment.State `json:"state"`
}

type Ack struct {
	ID string `json:"id"`
}

// NodeInventory carries one machine resource snapshot as opaque JSON.
type NodeInventory struct {
	Overview json.RawMessage `json:"overview"`
}

// An empty ack is the original successful response. Retry requests the latest
// snapshot again, while Rejected permanently discards an invalid snapshot.
type NodeInventoryAck struct {
	Retry    bool `json:"retry,omitempty"`
	Rejected bool `json:"rejected,omitempty"`
}

type NodeOperation struct {
	ID        string          `json:"id"`
	ExpiresMS int64           `json:"expires_ms"`
	Request   nodeops.Request `json:"request"`
}

type NodeOperationResult struct {
	ID     string          `json:"id"`
	State  nodeops.State   `json:"state"`
	Error  string          `json:"error,omitempty"`
	Result *nodeops.Result `json:"result,omitempty"`
}

func Encode(kind string, body any) (Message, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	return Message{Kind: kind, Body: encoded}, nil
}

func Decode(m Message, kind string, dst any) error {
	if m.Kind != kind {
		return errors.New("unexpected Agent message kind")
	}
	dec := json.NewDecoder(bytes.NewReader(m.Body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.New("expected a single Agent message body")
	}
	return nil
}

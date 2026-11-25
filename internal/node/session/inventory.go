package session

import (
	"context"
	"errors"

	"github.com/maxtanforge/moori/internal/core/deployment"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

type inventoryProgress struct {
	epoch, after string
	complete     bool
}

// sendLedgerInventory replays the execution ledger for recovery; distinct
// from sendMachineInventory, which reports Docker resources.
func (c Client) sendLedgerInventory(ctx context.Context, ch messageChannel, reply agentwire.Message, progress *inventoryProgress) error {
	var recovery agentwire.Recovery
	if err := agentwire.Decode(reply, agentwire.KindRecovery, &recovery); err != nil {
		return err
	}
	if recovery.Epoch == "" || len(recovery.Epoch) > 128 {
		return errors.New("invalid recovery epoch")
	}
	if recovery.Epoch != progress.epoch {
		*progress = inventoryProgress{epoch: recovery.Epoch}
	}
	if progress.complete {
		return nil
	}
	entries, err := c.Session.Ledger.Inventory(ctx, progress.after)
	if err != nil {
		return err
	}
	last := len(entries) < deployment.InventoryPageSize
	request, err := agentwire.Encode(agentwire.KindInventory, agentwire.Inventory{Epoch: recovery.Epoch, Records: entries, Complete: last})
	if err != nil {
		return err
	}
	response, err := exchange(ch, request)
	if err != nil {
		return err
	}
	var ack agentwire.Ack
	if err := agentwire.Decode(response, agentwire.KindInventoryAck, &ack); err != nil {
		return err
	}
	if ack.ID != recovery.Epoch {
		return errors.New("inventory acknowledgement does not match recovery epoch")
	}
	if len(entries) > 0 {
		progress.after = entries[len(entries)-1].Execution
	}
	progress.complete = last
	return nil
}

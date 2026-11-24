package session

import (
	"errors"
	"os"
	"time"

	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func (c Client) sendOutput(ch messageChannel) error {
	if c.Logs == nil {
		return nil
	}
	if c.outputAfter != nil && time.Now().Before(*c.outputAfter) {
		return nil
	}
	entry, err := c.Logs.Peek()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		c.Log.Debug("deployment output unavailable", "err", err)
		return nil
	}
	m, err := agentwire.Encode(agentwire.KindOutput, entry)
	if err != nil {
		return err
	}
	response, err := exchange(ch, m)
	if err != nil {
		return err
	}
	var ack agentwire.OutputAck
	if err := agentwire.Decode(response, agentwire.KindOutputAck, &ack); err != nil {
		return err
	}
	return c.acknowledgeOutput(entry.Execution, ack)
}

func (c Client) acknowledgeOutput(execution string, ack agentwire.OutputAck) error {
	if ack.ID != execution {
		return errors.New("deployment output acknowledgement does not match execution")
	}
	if ack.Retry {
		if c.outputAfter != nil {
			*c.outputAfter = time.Now().Add(30 * time.Second)
		}
		c.Log.Warn("control plane could not retain deployment output; retrying", "execution", execution)
		return nil
	}
	if !ack.Retained {
		c.Log.Debug("control plane declined deployment output", "execution", execution)
	}
	if err := c.Logs.Remove(execution); err != nil {
		c.Log.Debug("deployment output spool cleanup failed", "execution", execution, "err", err)
	}
	return nil
}

package session

import (
	"time"

	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func (c Client) diagnostics(ch messageChannel, nextSample *time.Time) error {
	if c.Collect != nil && !time.Now().Before(*nextSample) {
		*nextSample = time.Now().Add(30 * time.Second)
		sample, err := c.Collect()
		if err != nil {
			c.Log.Debug("host sample unavailable", "err", err)
		} else {
			m, err := agentwire.Encode(agentwire.KindHostSample, sample)
			if err != nil {
				return err
			}
			reply, err := exchange(ch, m)
			if err != nil {
				return err
			}
			if err := agentwire.Decode(reply, agentwire.KindHostSampleAck, &struct{}{}); err != nil {
				return err
			}
		}
	}
	return c.sendOutput(ch)
}

package session

import (
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/maxtanforge/moori/internal/platform/execlog"
	"github.com/maxtanforge/moori/internal/transport/agentwire"
)

func TestOutputRetryPreservesSpoolUntilRetained(t *testing.T) {
	logs, err := execlog.Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	entry := execlog.Entry{Execution: "execution", Text: "diagnostic"}
	if err := logs.Put(entry); err != nil {
		t.Fatal(err)
	}
	c := Client{Logs: logs, Log: slog.Default(), outputAfter: new(time.Time)}
	if err := c.acknowledgeOutput(entry.Execution, agentwire.OutputAck{ID: entry.Execution, Retry: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := logs.Read(entry.Execution); err != nil {
		t.Fatal("retry deleted spool", err)
	}
	if !c.outputAfter.After(time.Now()) {
		t.Fatal("missing retry backoff")
	}
	if err := c.acknowledgeOutput(entry.Execution, agentwire.OutputAck{ID: "other", Retained: true}); err == nil {
		t.Fatal("wrong acknowledgement accepted")
	}
	if err := c.acknowledgeOutput(entry.Execution, agentwire.OutputAck{ID: entry.Execution, Retained: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := logs.Read(entry.Execution); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retained output not removed", err)
	}
	if err := logs.Put(entry); err != nil {
		t.Fatal(err)
	}
	if err := c.acknowledgeOutput(entry.Execution, agentwire.OutputAck{ID: entry.Execution}); err != nil {
		t.Fatal(err)
	}
	if _, err := logs.Read(entry.Execution); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("permanent rejection not removed", err)
	}
}

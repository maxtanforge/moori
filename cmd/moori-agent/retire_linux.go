package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"github.com/maxtanforge/moori/internal/node/ledger"
)

func retireExecution(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("retire-execution", flag.ContinueOnError)
	db := f.String("db", "", "existing Agent ledger")
	id := f.String("execution", "", "execution ID in reconciliation")
	operator := f.String("operator", "", "operator identity for the ledger")
	note := f.String("note", "", "Docker inspection and verification that the operation stopped")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *db == "" || *id == "" || *operator == "" || *note == "" || f.NArg() != 0 {
		return errors.New("retire-execution requires -db, -execution, -operator and -note")
	}
	if _, err := os.Stat(*db); err != nil {
		return err
	}
	lock, err := lockLedger(*db)
	if err != nil {
		return err
	}
	defer lock.Close()
	l, err := ledger.Open(ctx, *db)
	if err != nil {
		return err
	}
	defer l.Close()
	return l.RetireInterrupted(ctx, *id, *operator, *note)
}

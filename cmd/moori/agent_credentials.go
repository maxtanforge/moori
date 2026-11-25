package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/maxtanforge/moori/internal/control/storage"
	"io"
	"time"
)

func agentCredentialCommand(ctx context.Context, command string, args []string, out, diagnostics io.Writer) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	db := f.String("db", defaultDB(), "SQLite database")
	agent := f.String("agent", "", "Agent ID")
	var ttl time.Duration
	if command == "issue-agent-credential" {
		f.DurationVar(&ttl, "ttl", 365*24*time.Hour, "credential lifetime, up to 10 years")
	}
	if err := f.Parse(args); err != nil {
		return err
	}
	if *agent == "" || f.NArg() != 0 {
		return errors.New("Agent credential command requires -agent")
	}
	s, err := storage.Open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close()
	switch command {
	case "issue-agent-credential":
		token, generation, err := s.IssueAgentCredential(ctx, *agent, ttl)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(diagnostics, "Agent %s generation %d; credential is written to stdout once.\n", *agent, generation); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, token)
		return err
	case "revoke-agent-credential":
		return s.RevokeAgentCredential(ctx, *agent)
	default:
		return errors.New("unknown Agent credential command")
	}
}

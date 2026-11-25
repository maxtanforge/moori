package main

import (
	"context"
	"errors"
	"flag"

	"github.com/maxtanforge/moori/internal/control/storage"
)

func parseMaintenance(f *flag.FlagSet, command string, args []string) (localAction, error) {
	switch command {
	case "abandon-deployment":
		id := f.String("id", "", "deployment ID")
		operator := f.String("operator", "", "operator identity")
		note := f.String("note", "", "evidence that old host Docker cannot execute further mutations")
		fenced := f.Bool("host-fenced", false, "confirm old host Docker and Agent are stopped or physically fenced")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		if *id == "" || *operator == "" || *note == "" || !*fenced {
			return nil, errors.New("requires -id, -operator, -note and -host-fenced; rotate the Agent certificate generation first")
		}
		return func(ctx context.Context, s *storage.Store) error {
			return s.AbandonFencedDeployment(ctx, *id, *operator, *note, *fenced)
		}, nil
	case "assign-target":
		project := f.String("project", "", "project ID")
		target := f.String("target", "", "target ID")
		agent := f.String("agent", "", "registered Agent ID")
		generation := f.Int64("generation", 0, "current registered generation")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error {
			return s.ReassignTarget(ctx, *project, *target, *agent, *generation)
		}, nil
	}
	return nil, errors.New(controlUsage)
}

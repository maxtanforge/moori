package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/control/storage"
	"github.com/maxtanforge/moori/internal/core/release"
)

const controlUsage = "usage: moori <command> [options]\ncommands: retire-local-execution, issue-agent-credential, revoke-agent-credential, abandon-deployment, assign-target, serve, register-target, register-container-target, publish-release, publish-template, issue-ci-token, revoke-ci-token, create-user, grant-platform-admin, revoke-platform-admin, cancel-deployment, rollback-deployment, backup-db, verify-backup, prepare-restore, restore-status, restore-inventory, restore-release-target, agent-health, init-secret-key, put-secret, revoke-secret, set-target-secrets"

func localCommand(ctx context.Context, command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("db", defaultDB(), "local control database")
	action, err := parseLocal(flags, command, args)
	if err != nil {
		return err
	}
	store, err := storage.Open(ctx, *path)
	if err != nil {
		return err
	}
	defer store.Close()
	return action(ctx, store)
}

type localAction func(context.Context, *storage.Store) error

func parseLocal(f *flag.FlagSet, command string, args []string) (localAction, error) {
	switch command {
	case "abandon-deployment", "assign-target":
		return parseMaintenance(f, command, args)
	case "put-secret", "revoke-secret", "set-target-secrets":
		return parseSecretCommand(f, command, args)
	case "rollback-deployment":
		source := f.String("from", "", "successful historical deployment ID")
		key := f.String("key", "", "idempotency key for this rollback request")
		operator := f.String("operator", "", "operator identity")
		note := f.String("note", "", "reason for rollback")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		if *source == "" || *key == "" || *operator == "" || *note == "" {
			return nil, errors.New("rollback-deployment requires -from, -key, -operator and -note")
		}
		return func(ctx context.Context, s *storage.Store) error {
			id, err := s.RequestRollback(ctx, storage.RollbackRequest{ID: "dep-" + rand.Text(), Source: *source, Key: *key, Operator: *operator, Note: *note})
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(struct {
				ID string `json:"id"`
			}{ID: id})
		}, nil
	case "cancel-deployment":
		id := f.String("id", "", "deployment ID")
		operator := f.String("operator", "", "operator identity")
		note := f.String("note", "", "reason for cancellation")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		if *id == "" || *operator == "" || *note == "" {
			return nil, errors.New("cancel-deployment requires -id, -operator and -note")
		}
		return func(ctx context.Context, s *storage.Store) error {
			result, err := s.RequestCancel(ctx, *id, *operator, *note)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}, nil
	case "register-target":
		project := f.String("project", "", "project ID")
		target := f.String("target", "", "target ID")
		agent := f.String("agent", "", "bound Agent ID")
		generation := f.Int64("generation", 1, "bound Agent generation")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error {
			return s.RegisterAssignedTarget(ctx, *project, *target, *agent, *generation)
		}, nil
	case "register-container-target":
		project := f.String("project", "", "project ID")
		target := f.String("target", "", "new CI container target ID")
		agent := f.String("agent", "", "bound Agent ID or @local")
		generation := f.Int64("generation", 1, "bound Agent generation")
		name := f.String("container", "", "Docker container name")
		repository := f.String("repository", "", "fully qualified image repository without tag or digest")
		registryID := f.String("registry", "", "optional shared registry ID")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error {
			return s.RegisterContainerTarget(ctx, *project, *target, *agent, *generation, *name, *repository, *registryID)
		}, nil
	case "publish-release":
		project := f.String("project", "", "project ID")
		id := f.String("release", "", "release ID")
		path := f.String("compose", "", "Compose file containing digest-pinned images")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error { return publishFile(ctx, s, *project, *id, *path) }, nil
	case "publish-template":
		project := f.String("project", "", "project ID")
		target := f.String("target", "", "target ID")
		id := f.String("template", "", "immutable template version")
		path := f.String("compose", "", "Compose template file")
		allowed := f.String("allow-images", "", "comma-separated service names with replaceable images")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error {
			raw, err := readCompose(*path)
			if err != nil {
				return err
			}
			names := strings.Split(*allowed, ",")
			for i := range names {
				names[i] = strings.TrimSpace(names[i])
			}
			return s.PublishTemplate(ctx, *project, *target, *id, string(raw), names)
		}, nil
	case "issue-ci-token":
		id := f.String("id", "", "unique credential ID")
		project := f.String("project", "", "permitted project")
		target := f.String("target", "", "permitted target")
		ttl := f.Duration("ttl", 24*time.Hour, "credential lifetime")
		publish := f.Bool("allow-publish", false, "allow publishing immutable releases on this target")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error {
			issue := s.IssueServiceCredential
			if *publish {
				issue = s.IssuePublishingCredential
			}
			token, err := issue(ctx, *id, *project, *target, time.Now().Add(*ttl))
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(os.Stdout, token)
			return err
		}, nil
	case "revoke-ci-token":
		id := f.String("id", "", "credential ID")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error { return s.RevokeServiceCredential(ctx, *id) }, nil
	case "create-user":
		username := f.String("username", "", "username; supply the initial password followed by a newline on stdin")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error {
			password, err := io.ReadAll(io.LimitReader(os.Stdin, 1027))
			if err != nil {
				return err
			}
			line := strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")
			if len(password) >= 1027 || strings.ContainsAny(line, "\r\n") {
				return errors.New("expected one password line on stdin")
			}
			id, err := s.CreateHumanUser(ctx, *username, line)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(os.Stdout, id)
			return err
		}, nil
	case "grant-platform-admin", "revoke-platform-admin":
		username := f.String("username", "", "existing human username")
		if err := parseFlags(f, args); err != nil {
			return nil, err
		}
		return func(ctx context.Context, s *storage.Store) error {
			return s.SetPlatformAdmin(ctx, *username, command == "grant-platform-admin")
		}, nil
	default:
		return nil, errors.New(controlUsage)
	}
}

func parseFlags(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return nil
}

func publishFile(ctx context.Context, s *storage.Store, project, id, path string) error {
	document, err := readCompose(path)
	if err != nil {
		return err
	}
	artifact, err := release.Compile(ctx, document, nil, nil, nil)
	if err != nil {
		return err
	}
	return s.PublishRelease(ctx, project, id, artifact)
}

func readCompose(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const maxInput = 256 << 10
	return io.ReadAll(io.LimitReader(file, maxInput+1))
}

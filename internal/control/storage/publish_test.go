package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServicePublicationStaysOnAuthorizedTarget(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	for _, target := range []string{"one", "two"} {
		if err := s.RegisterTarget(ctx, "project", target); err != nil {
			t.Fatal(err)
		}
	}
	expires := time.Now().Add(time.Hour)
	deployOnly, err := s.IssueServiceCredential(ctx, "default", "project", "one", expires)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := s.IssuePublishingCredential(ctx, "publisher", "project", "one", expires)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.IssueServiceCredential(ctx, "other", "project", "two", expires)
	if err != nil {
		t.Fatal(err)
	}
	image := "nginx@sha256:" + strings.Repeat("a", 64)
	input := Publication{Project: "project", Target: "one", ID: "r1", Compose: "services: {web: {image: '" + image + "'}}"}
	if _, err := s.PublishForService(ctx, deployOnly, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("default credential could publish: %v", err)
	}
	first, err := s.PublishForService(ctx, publisher, input)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.PublishForService(ctx, publisher, input); err != nil || again != first {
		t.Fatalf("publication replay: %+v %v", again, err)
	}
	input.Compose = strings.ReplaceAll(input.Compose, strings.Repeat("a", 64), strings.Repeat("b", 64))
	if _, err := s.PublishForService(ctx, publisher, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("modified immutable release: %v", err)
	}
	if _, err := s.AdmitPublishedRelease(ctx, other, Admission{ID: "cross", Project: "project", Target: "two", Release: "r1", Key: "cross"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("another target saw private release: %v", err)
	}
	if _, err := s.AdmitPublishedRelease(ctx, deployOnly, Admission{ID: "local", Project: "project", Target: "one", Release: "r1", Key: "local"}); err != nil {
		t.Fatalf("bound target could not deploy its release: %v", err)
	}
	template := "services: {web: {image: nginx:placeholder}, sidecar: {image: '" + image + "'}}"
	if err := s.PublishTemplate(ctx, "project", "one", "t1", template, []string{"web"}); err != nil {
		t.Fatal(err)
	}
	input.ID, input.Compose, input.Template = "r2", "", "t1"
	input.Images = map[string]string{"sidecar": image}
	if _, err := s.PublishForService(ctx, publisher, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unauthorized image override: %v", err)
	}
	input.Images = map[string]string{"web": image}
	if _, err := s.PublishForService(ctx, publisher, input); err != nil {
		t.Fatal(err)
	}
}

package release

import (
	"context"
	"strings"
	"testing"
)

func TestCompileMemoReturnsIdenticalResults(t *testing.T) {
	ctx := context.Background()
	fixture := "services:\n  web:\n    image: nginx@sha256:" + strings.Repeat("a", 64) + "\n"
	first, err := Compile(ctx, []byte(fixture), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(ctx, []byte(fixture), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.document != second.document || first.digest != second.digest || first.external != second.external {
		t.Fatal("memoized compile diverged from the first result")
	}
	if _, err := Compile(ctx, []byte("services: {}"), nil, nil, nil); err == nil {
		t.Fatal("empty service list should not compile")
	}
	if _, err := Compile(ctx, []byte("services: {}"), nil, nil, nil); err == nil {
		t.Fatal("memoized error should repeat identically")
	}
}

func TestCompileMemoBypassesNonDeterministicAndCanceledCalls(t *testing.T) {
	ctx := context.Background()
	fixture := "services:\n  web:\n    image: nginx@sha256:" + strings.Repeat("b", 64) + "\n"
	plain, err := Compile(ctx, []byte(fixture), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// An empty override set is semantically the unoverridden compile but takes
	// the uncached path; it must still produce the same release.
	empty, err := Compile(ctx, []byte(fixture), map[string]string{}, []string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.document != plain.document || empty.digest != plain.digest {
		t.Fatal("empty overrides diverged from the plain compile")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Compile(canceled, []byte(fixture), nil, nil, nil); err == nil {
		t.Fatal("canceled context should fail compilation")
	}
	if _, err := Compile(ctx, []byte(fixture), nil, nil, nil); err != nil {
		t.Fatal("cancellation must not poison the memo:", err)
	}

	if _, err := CompileExisting(canceled, []byte(fixture)); err == nil {
		t.Fatal("canceled context should fail CompileExisting")
	}
	if _, err := CompileExisting(ctx, []byte(fixture)); err != nil {
		t.Fatal("cancellation must not poison CompileExisting:", err)
	}
}

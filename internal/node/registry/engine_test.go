package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/docker/docker/client"
	"github.com/maxtanforge/moori/internal/core/nodeops"
	"github.com/maxtanforge/moori/internal/core/registry"
)

type pullTransport func(*http.Request) (*http.Response, error)

func (f pullTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestPullTimeoutTracksDockerActivity(t *testing.T) {
	for _, stall := range []bool{false, true} {
		name := "continuous progress"
		if stall {
			name = "stalled stream"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := pullTransport(func(req *http.Request) (*http.Response, error) {
					var body io.ReadCloser
					if req.Method == http.MethodPost {
						reader, writer := io.Pipe()
						body = reader
						go func() {
							defer writer.Close()
							stop := context.AfterFunc(req.Context(), func() { writer.CloseWithError(req.Context().Err()) })
							defer stop()
							for range 3 {
								if _, err := io.WriteString(writer, "{\"status\":\"Downloading\"}\n"); err != nil {
									return
								}
								if stall {
									<-req.Context().Done()
									return
								}
								time.Sleep(10 * time.Minute)
							}
							io.WriteString(writer, "{\"status\":\"Status: Downloaded newer image for alpine:latest\"}\n")
						}()
					} else {
						body = io.NopCloser(strings.NewReader(`{"Id":"sha256:abc"}`))
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
				})
				docker, err := client.NewClientWithOpts(client.WithVersion("1.48"), client.WithHTTPClient(&http.Client{Transport: transport}))
				if err != nil {
					t.Fatal(err)
				}
				defer docker.Close()
				ctx, cancel := context.WithTimeout(t.Context(), nodeops.RegistryPull.Timeout())
				defer cancel()
				err = (&Engine{client: docker}).Pull(ctx, "alpine:latest", "", registry.Credential{})
				if stall && !errors.Is(err, nodeops.ErrPullStalled) || !stall && err != nil {
					t.Fatal("unexpected pull result", err)
				}
			})
		})
	}
}

func TestInterruptedTagPullDoesNotConfirmExistingImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			io.WriteString(w, "{\"status\":\"Downloading\",\"id\":\"layer\"}\n")
			return
		}
		io.WriteString(w, `{"Id":"old-local-image"}`)
	}))
	defer server.Close()
	docker, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.48"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	if err := (&Engine{client: docker}).Pull(t.Context(), "alpine:latest", "", registry.Credential{}); !errors.Is(err, nodeops.ErrOutcomeUnknown) {
		t.Fatalf("interrupted update of an existing tag: %v", err)
	}
}

func TestEngineRejectsErrorInSuccessfulPullResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, err := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
		var cfg registry.Credential
		if err != nil || json.Unmarshal(auth, &cfg) != nil || cfg.Username != "robot" || cfg.Password != "token" || cfg.ServerAddress != "registry.example" {
			t.Error("expected registry auth header")
		}
		if r.URL.Query().Get("tag") != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Error("request did not pin the manifest digest")
		}
		_, _ = w.Write([]byte("{\"errorDetail\":{\"message\":\"sensitive upstream detail\"}}\n"))
	}))
	defer server.Close()
	docker, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.48"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	engine := Engine{client: docker}
	err = engine.Pull(context.Background(), "registry.example/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", registry.Credential{Username: "robot", Password: "token", ServerAddress: "registry.example"})
	if err == nil || err.Error() != "Docker could not pull the registry image" {
		t.Fatalf("pull error: %v", err)
	}
}

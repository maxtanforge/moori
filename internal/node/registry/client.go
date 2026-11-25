package registry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/maxtanforge/moori/internal/core/registry"
)

var requestID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type Client struct {
	token    string
	endpoint url.URL
	http     *http.Client
}

func NewCredentialClient(endpoint *url.URL, cfg *tls.Config, token string) (*Client, error) {
	if endpoint == nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Scheme != "wss" && endpoint.Scheme != "ws" ||
		token == "" || strings.ContainsAny(token, " \r\n\t") || cfg != nil && cfg.InsecureSkipVerify {
		return nil, errors.New("invalid Agent endpoint or credential")
	}
	if cfg == nil {
		cfg = &tls.Config{}
	} else {
		cfg = cfg.Clone()
	}
	cfg.MinVersion = max(cfg.MinVersion, tls.VersionTLS12)
	scheme := "https"
	if endpoint.Scheme == "ws" {
		scheme = "http"
	}
	transport := &http.Transport{TLSClientConfig: cfg, MaxIdleConnsPerHost: 1, ResponseHeaderTimeout: 5 * time.Second}
	httpClient := &http.Client{
		Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{endpoint: url.URL{Scheme: scheme, Host: endpoint.Host}, token: token, http: httpClient}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Fetch(ctx context.Context, execution, digest string) (map[string]registry.Credential, error) {
	if !requestID.MatchString(execution) || execution == "." || execution == ".." || len(digest) != 71 {
		return nil, registry.ErrDenied
	}
	var result map[string]registry.Credential
	if err := c.fetch(ctx, "/agent/v1/executions/"+execution+"/registry", digest, &result); err != nil {
		return nil, err
	}
	if result == nil || len(result) > 64 {
		return nil, registry.ErrDenied
	}
	if c.endpoint.Scheme != "https" && len(result) > 0 {
		return nil, registry.ErrSecureTransport
	}
	return result, nil
}

func (c *Client) FetchOperation(ctx context.Context, operation string) (registry.Credential, error) {
	var result registry.Credential
	if !requestID.MatchString(operation) || operation == "." || operation == ".." {
		return result, registry.ErrDenied
	}
	if err := c.fetch(ctx, "/agent/v1/operations/"+operation+"/registry", "", &result); err != nil {
		return registry.Credential{}, err
	}
	if c.endpoint.Scheme != "https" && result != (registry.Credential{}) {
		return registry.Credential{}, registry.ErrSecureTransport
	}
	return result, nil
}

func (c *Client) fetch(ctx context.Context, path, digest string, dest any) error {
	endpoint := c.endpoint
	endpoint.Path = path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return registry.ErrDenied
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if digest != "" {
		req.Header.Set("X-Moori-Snapshot-Digest", digest)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return registry.ErrRetry
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusPreconditionFailed:
		return registry.ErrAmbiguous
	case http.StatusUpgradeRequired:
		return registry.ErrSecureTransport
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusBadRequest:
		return registry.ErrDenied
	case http.StatusNotFound:
		return registry.ErrDenied
	default:
		return registry.ErrRetry
	}
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	defer clear(data)
	if err != nil || len(data) > limit {
		return registry.ErrRetry
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return registry.ErrDenied
	}
	return nil
}

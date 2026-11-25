// Package secrets fetches and stages versioned secret bundles for Docker.
package secrets

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
)

var ErrDenied = errors.New("required secret versions were denied or unavailable")
var ErrRetry = errors.New("secret provider temporarily unavailable")
var executionID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type Client struct {
	token    string
	endpoint url.URL
	http     *http.Client
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Fetch(ctx context.Context, execution, digest string) (map[string][]byte, error) {
	if !executionID.MatchString(execution) || len(digest) != 71 {
		return nil, ErrDenied
	}
	endpoint := c.endpoint
	endpoint.Path = "/agent/v1/executions/" + execution + "/secrets"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return nil, ErrRetry
	}
	req.Header.Set("X-Moori-Snapshot-Digest", digest)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrRetry
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict:
		return nil, ErrDenied
	default:
		return nil, ErrRetry
	}
	const limit = 512 << 10
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	defer clear(data)
	if err != nil || len(data) > limit {
		return nil, ErrRetry
	}
	var result map[string][]byte
	if err := json.Unmarshal(data, &result); err != nil || len(result) == 0 {
		for _, value := range result {
			clear(value)
		}
		return nil, ErrDenied
	}
	return result, nil
}

func NewCredentialClient(endpoint *url.URL, cfg *tls.Config, token string) (*Client, error) {
	if endpoint == nil || endpoint.Host == "" || endpoint.User != nil || (endpoint.Scheme != "ws" && endpoint.Scheme != "wss") || token == "" || strings.ContainsAny(token, " \r\n\t") || cfg != nil && cfg.InsecureSkipVerify {
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
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, MaxIdleConnsPerHost: 1, ResponseHeaderTimeout: 5 * time.Second}, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{endpoint: url.URL{Scheme: scheme, Host: endpoint.Host}, http: client, token: token}, nil
}

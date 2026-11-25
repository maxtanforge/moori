package imagearchive

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var operationID = regexp.MustCompile("^[a-f0-9]{32}$")

type Client struct {
	endpoint url.URL
	token    string
	http     *http.Client
}

func NewClient(endpoint *url.URL, config *tls.Config, token string) (*Client, error) {
	if endpoint == nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Scheme != "wss" ||
		token == "" || strings.ContainsAny(token, " \r\n\t") || config != nil && config.InsecureSkipVerify {
		return nil, errors.New("remote image archives require a verified WSS Agent endpoint")
	}
	if config == nil {
		config = &tls.Config{}
	} else {
		config = config.Clone()
	}
	config.MinVersion = max(config.MinVersion, tls.VersionTLS12)
	return &Client{
		endpoint: url.URL{Scheme: "https", Host: endpoint.Host}, token: token,
		http: &http.Client{Transport: &http.Transport{TLSClientConfig: config}, Timeout: Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, id string, body io.Reader) (*http.Response, error) {
	if !operationID.MatchString(id) {
		return nil, ErrUnavailable
	}
	endpoint := c.endpoint
	endpoint.Path = "/agent/v1/operations/" + id + "/image-archive"
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-tar")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		resp.Body.Close()
		return nil, ErrTooLarge
	}
	if method == http.MethodGet && resp.StatusCode != http.StatusOK || method == http.MethodPost && resp.StatusCode != http.StatusNoContent {
		resp.Body.Close()
		return nil, ErrUnavailable
	}
	return resp, nil
}

func (c *Client) Send(ctx context.Context, id string, source io.ReadCloser) error {
	defer source.Close()
	resp, err := c.request(ctx, http.MethodPost, id, source)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func (c *Client) Receive(ctx context.Context, id string, consume func(io.Reader) error) error {
	resp, err := c.request(ctx, http.MethodGet, id, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return consumeArchive(resp.Body, consume)
}

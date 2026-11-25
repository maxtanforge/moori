// Package logstream relays authorized live logs over the control plane's HTTPS endpoint.
package logstream

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/maxtanforge/moori/internal/core/nodeops"
	wire "github.com/maxtanforge/moori/internal/transport/logstream"
)

const logPath = "/agent/v1/log-stream/"
const maxStreams = 4

var (
	ticketID       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	errUnavailable = errors.New("control log stream is unavailable")
)

type Client struct {
	endpoint url.URL
	token    string
	http     *http.Client
}

func NewClient(endpoint *url.URL, config *tls.Config, token string) (*Client, error) {
	if endpoint == nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Scheme != "wss" ||
		token == "" || strings.ContainsAny(token, " \r\n\t") || config != nil && config.InsecureSkipVerify {
		return nil, errors.New("remote logs require a verified WSS Agent endpoint")
	}
	if config == nil {
		config = &tls.Config{}
	} else {
		config = config.Clone()
	}
	config.MinVersion = max(config.MinVersion, tls.VersionTLS12)
	transport := &http.Transport{
		DialContext:       (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, TLSClientConfig: config, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: wire.PollTimeout + 10*time.Second, IdleConnTimeout: 90 * time.Second,
		MaxIdleConnsPerHost: maxStreams + 1,
	}
	return &Client{
		endpoint: url.URL{Scheme: "https", Host: endpoint.Host}, token: token,
		http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

// Run claims one ticket at a time and publishes a bounded number in parallel.
// It waits for outstanding Docker readers to close after cancellation.
func (c *Client) Run(ctx context.Context, follow func(context.Context, string, int, bool, bool) (io.ReadCloser, error)) error {
	if follow == nil {
		return errors.New("Docker log stream reader is unavailable")
	}
	var workers sync.WaitGroup
	defer workers.Wait()
	slots := make(chan struct{}, maxStreams)
	backoff := time.Second
	for ctx.Err() == nil {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		ticket, err := c.next(ctx)
		if err != nil {
			<-slots
			wait(ctx, backoff)
			backoff = min(2*backoff, 30*time.Second)
			continue
		}
		backoff = time.Second
		if ticket == nil {
			<-slots
			wait(ctx, time.Second)
			continue
		}
		workers.Go(func() {
			defer func() { <-slots }()
			c.publish(ctx, *ticket, follow)
		})
	}
	return ctx.Err()
}

func (c *Client) next(ctx context.Context) (*wire.Ticket, error) {
	ctx, cancel := context.WithTimeout(ctx, wire.PollTimeout+15*time.Second)
	defer cancel()
	reply, err := c.request(ctx, http.MethodGet, "next", nil)
	if err != nil {
		return nil, err
	}
	defer reply.Body.Close()
	if reply.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if reply.StatusCode != http.StatusOK {
		return nil, errUnavailable
	}
	const limit = 2048
	data, err := io.ReadAll(io.LimitReader(reply.Body, limit+1))
	var ticket wire.Ticket
	if err != nil || len(data) > limit || json.Unmarshal(data, &ticket) != nil || !ticketID.MatchString(ticket.ID) {
		return nil, errUnavailable
	}
	if err := (nodeops.Request{Action: nodeops.ContainerLogs, Target: ticket.Container, Tail: ticket.Tail, Timestamps: ticket.Timestamps}).Validate(); err != nil {
		return nil, errUnavailable
	}
	return &ticket, nil
}

func (c *Client) publish(ctx context.Context, ticket wire.Ticket, follow func(context.Context, string, int, bool, bool) (io.ReadCloser, error)) {
	ctx, cancel := context.WithTimeout(ctx, wire.StreamTimeout+5*time.Second)
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		source, err := follow(ctx, ticket.Container, ticket.Tail, ticket.Timestamps, ticket.AllLogRuns)
		if source == nil {
			return
		}
		closeSource := sync.OnceFunc(func() { _ = source.Close() })
		stop := context.AfterFunc(ctx, closeSource)
		defer func() { stop(); closeSource() }()
		if err == nil {
			_, _ = io.Copy(writer, source)
		}
	}()

	// Send the request even before Docker opens the stream. An early response
	// lets the broker cancel a request with no available log bytes.
	if reply, err := c.request(ctx, http.MethodPost, ticket.ID, reader); err == nil {
		_ = reply.Body.Close()
	}
	cancel()
	_ = reader.Close()
	<-done
}

func (c *Client) request(ctx context.Context, method, suffix string, body io.Reader) (*http.Response, error) {
	endpoint := c.endpoint
	endpoint.Path = logPath + suffix
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, errUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	reply, err := c.http.Do(req)
	if err != nil {
		return nil, errUnavailable
	}
	return reply, nil
}

func wait(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// Package agentwire provides the authenticated WebSocket transport for Agents.
package agentwire

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	protocol     = "moori.agent.v1"
	maxFrame     = 1 << 20
	readTimeout  = 45 * time.Second
	writeTimeout = 10 * time.Second
)

type Peer struct {
	ID         string
	Generation int64
}

type Message struct {
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body"`
}

type Channel struct {
	conn   *websocket.Conn
	writer sync.Mutex
}

func channel(c *websocket.Conn) *Channel {
	c.SetReadLimit(maxFrame)
	return &Channel{conn: c}
}

func (c *Channel) Close() error { return c.conn.Close() }

// Receive is called from a single reader; heartbeats are ordinary messages.
func (c *Channel) Receive() (Message, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return Message{}, err
	}
	typ, raw, err := c.conn.ReadMessage()
	if err != nil {
		return Message{}, err
	}
	if typ != websocket.TextMessage {
		return Message{}, errors.New("Agent messages must use text frames")
	}
	var m Message
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Message{}, err
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return Message{}, errors.New("expected one Agent message")
	}
	if !m.valid() {
		return Message{}, errors.New("invalid Agent message")
	}
	return m, nil
}

func (m Message) valid() bool { return m.Kind != "" && len(m.Kind) <= 64 && len(m.Body) > 0 }

func (c *Channel) Send(m Message) error {
	if !m.valid() || len(m.Body) > maxFrame {
		return errors.New("invalid or oversized Agent message")
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) > maxFrame {
		return errors.New("Agent message exceeds maximum size")
	}
	c.writer.Lock()
	defer c.writer.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// Handler authenticates each connection before accepting Agent messages.
type Handler struct {
	Authenticate func(*http.Request) (Peer, error)
	Serve        func(context.Context, Peer, *Channel)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	if h.Serve == nil || h.Authenticate == nil {
		http.Error(w, "Agent service unavailable", 503)
		return
	}
	if r.Header.Get("Origin") != "" || !slices.Contains(websocket.Subprotocols(r), protocol) {
		http.Error(w, "unsupported Agent connection", 400)
		return
	}
	ctx := r.Context()
	peer, err := h.Authenticate(r)
	if err != nil || peer.ID == "" || peer.Generation <= 0 {
		http.Error(w, "Agent identity rejected", 401)
		return
	}
	upgrader := websocket.Upgrader{Subprotocols: []string{protocol}, HandshakeTimeout: writeTimeout}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	h.Serve(ctx, peer, channel(c))
}

// DialCredential connects using a bearer credential. WS is for trusted private networks;
// WSS verifies the gateway certificate using system roots unless cfg overrides them.
func DialCredential(ctx context.Context, target string, cfg *tls.Config, token string) (*Channel, error) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "ws" && u.Scheme != "wss") || token == "" || strings.ContainsAny(token, " \r\n\t") || cfg != nil && cfg.InsecureSkipVerify {
		return nil, errors.New("invalid Agent endpoint or credential")
	}
	if cfg == nil {
		cfg = &tls.Config{}
	} else {
		cfg = cfg.Clone()
	}
	cfg.MinVersion = max(cfg.MinVersion, tls.VersionTLS12)
	d := websocket.Dialer{TLSClientConfig: cfg, HandshakeTimeout: writeTimeout, Subprotocols: []string{protocol}}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	c, resp, err := d.DialContext(ctx, target, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, err
	}
	if c.Subprotocol() != protocol {
		c.Close()
		return nil, errors.New("control plane did not negotiate the Agent protocol")
	}
	return channel(c), nil
}

// Package execstream connects the agent to the control plane's terminal
// relay and drives interactive docker exec sessions in the local daemon.
package execstream

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	wire "github.com/maxtanforge/moori/internal/transport/execstream"
)

const execPath = "/agent/v1/exec-stream"

const (
	outputChunk  = 16 << 10
	writeTimeout = 5 * time.Second
	frameTimeout = 10 * time.Second
	pongTimeout  = wire.PongTimeout
	pingInterval = wire.PingInterval
	dialBackoff  = time.Second
	maxBackoff   = 30 * time.Second
)

// Session is one live exec connection inside a container.
type Session = wire.Terminal

// Starter creates the exec session a browser asked for.
type Starter = wire.StartTerminal

// A Client dials the terminal relay for wss Agent endpoints only.
type Client struct {
	endpoint url.URL
	token    string
	dialer   websocket.Dialer
}

// NewClient mirrors the live log client: terminals require a verified
// WSS endpoint and a bearer credential.
func NewClient(endpoint *url.URL, config *tls.Config, token string) (*Client, error) {
	if endpoint == nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Scheme != "wss" ||
		token == "" || strings.ContainsAny(token, " \r\n\t") || config != nil && config.InsecureSkipVerify {
		return nil, errors.New("remote terminals require a verified WSS Agent endpoint")
	}
	if config == nil {
		config = &tls.Config{}
	} else {
		config = config.Clone()
	}
	config.MinVersion = max(config.MinVersion, tls.VersionTLS12)
	return &Client{
		endpoint: url.URL{Scheme: "https", Host: endpoint.Host}, token: token,
		dialer: websocket.Dialer{TLSClientConfig: config, HandshakeTimeout: wire.WriteTimeout},
	}, nil
}

// Run dials the relay until the context ends. Every accepted connection
// serves exactly one terminal session.
func (c *Client) Run(ctx context.Context, start Starter) error {
	if start == nil {
		return errors.New("docker exec sessions are unavailable")
	}
	backoff := dialBackoff
	for ctx.Err() == nil {
		err := c.serve(ctx, start)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			backoff = dialBackoff
		} else {
			wait(ctx, backoff)
			backoff = min(2*backoff, maxBackoff)
		}
	}
	return ctx.Err()
}

// serve runs one relay connection end to end. A nil return means the relay
// closed a finished session; the caller redials with the short backoff.
func (c *Client) serve(ctx context.Context, start Starter) error {
	target := c.endpoint
	target.Scheme = "wss"
	target.Path = execPath
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.token)
	conn, reply, err := c.dialer.DialContext(ctx, target.String(), header)
	if err != nil {
		if reply != nil && reply.Body != nil {
			_ = reply.Body.Close()
		}
		return err
	}
	defer conn.Close()
	conn.SetReadLimit(64 << 10)

	var writer sync.Mutex
	send := func(messageType int, data []byte) error {
		writer.Lock()
		defer writer.Unlock()
		if err := conn.SetWriteDeadline(time.Now().Add(frameTimeout)); err != nil {
			return err
		}
		return conn.WriteMessage(messageType, data)
	}
	sendJSON := func(payload any) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return send(websocket.TextMessage, data)
	}

	// Keepalive: our pings keep the connection idle-open; the server's pongs
	// extend the read deadline.
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongTimeout))
	})
	_ = conn.SetReadDeadline(time.Now().Add(pongTimeout))
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if err := send(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}()

	// The first frame carries the exec parameters.
	kind, data, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	if kind != websocket.TextMessage {
		return errors.New("unexpected terminal handshake frame")
	}
	var params wire.OpenParams
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&params); err != nil || params.Validate() != nil {
		_ = sendJSON(wire.Ack{Error: "invalid terminal parameters"})
		return err
	}
	sess, err := start(ctx, params.Container, params.Cols, params.Rows)
	if err != nil {
		_ = sendJSON(wire.Ack{Error: err.Error()})
		return err
	}
	defer sess.Close()
	if err := sendJSON(wire.Ack{OK: true}); err != nil {
		return err
	}

	// Output pump: node bytes become binary frames; a closed exec ends the
	// session with an eof control frame carrying the exit code.
	sessionEnded := make(chan struct{})
	var once sync.Once
	stopOutput := func() { once.Do(func() { close(sessionEnded) }) }
	go func() {
		defer stopOutput()
		buf := make([]byte, outputChunk)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				// send writes synchronously, so reusing the read buffer is safe.
				if sendErr := send(websocket.BinaryMessage, buf[:n]); sendErr != nil {
					return
				}
			}
			if err != nil {
				code := wire.TerminalExitCode(ctx, sess)
				_ = sendJSON(wire.EOF{EOF: true, ExitCode: code})
				return
			}
		}
	}()

	// Input loop: binary frames are keystrokes, text frames are resize
	// requests. The relay closes the connection when the session ends.
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		switch kind {
		case websocket.BinaryMessage:
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := sess.Write(data); err != nil {
				return err
			}
		case websocket.TextMessage:
			var resize wire.Resize
			if json.Unmarshal(data, &resize) == nil && resize.Resize.Cols > 0 && resize.Resize.Rows > 0 {
				if err := sess.Resize(ctx, resize.Resize.Cols, resize.Resize.Rows); err != nil {
					continue // a failed resize must not kill the session
				}
			}
		}
		select {
		case <-sessionEnded:
			return nil
		default:
		}
	}
}

func wait(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// execConn wraps the hijacked connection: reads go through the buffered
// reader so no early output is lost, writes reach the raw connection.
type execConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

func (c *execConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *execConn) Write(p []byte) (int, error) { return c.conn.Write(p) }
func (c *execConn) Close() error                { return c.conn.Close() }

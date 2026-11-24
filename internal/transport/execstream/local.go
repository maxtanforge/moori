package execstream

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/gorilla/websocket"
)

// Terminal is a Docker exec stream confined to the requested container.
type Terminal interface {
	io.ReadWriteCloser
	Resize(context.Context, int, int) error
	ExitCode(context.Context) int
}

type StartTerminal func(context.Context, string, int, int) (Terminal, error)

// ConnectLocal serves one browser directly without an Agent relay.
func (s *Session) ConnectLocal(ctx context.Context, browser *Connection, start StartTerminal) (err error) {
	if browser == nil || start == nil {
		s.Cancel()
		return ErrInvalid
	}
	s.attach(browser, true)
	defer func() { s.teardown(s.broker, err) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go keepalive(ctx, browser)
	input := make(chan frame, 4)
	go readPump(ctx, browser, input)

	openCtx, done := context.WithTimeout(ctx, ackTimeout)
	terminal, err := start(openCtx, s.Container, s.Cols, s.Rows)
	done()
	if err != nil {
		_ = browser.SendJSON(readyMessage{Error: "无法启动容器终端，请检查容器是否运行且包含 shell。"})
		return err
	}
	defer terminal.Close()
	s.start.Do(func() { close(s.started) })
	if err := browser.SendJSON(readyMessage{Ready: true}); err != nil {
		return err
	}

	output := make(chan error, 1)
	go func() {
		buf := make([]byte, 16<<10)
		for {
			n, readErr := terminal.Read(buf)
			if n > 0 {
				if err := browser.Relay(websocket.BinaryMessage, buf[:n]); err != nil {
					output <- err
					return
				}
			}
			if readErr != nil {
				output <- browser.SendJSON(EOF{EOF: true, ExitCode: TerminalExitCode(ctx, terminal)})
				return
			}
		}
	}()

	for {
		select {
		case err := <-output:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-input:
			if !ok {
				return ErrUnavailable
			}
			if msg.kind == websocket.BinaryMessage {
				if _, err := terminal.Write(msg.data); err != nil {
					return err
				}
			} else if msg.kind == websocket.TextMessage {
				var resize Resize
				if json.Unmarshal(msg.data, &resize) != nil {
					continue
				}
				cols, rows := resize.Resize.Cols, resize.Resize.Rows
				if cols < MinCols || cols > MaxCols || rows < MinRows || rows > MaxRows {
					continue
				}
				resizeCtx, done := context.WithTimeout(ctx, WriteTimeout)
				_ = terminal.Resize(resizeCtx, cols, rows)
				done()
			}
		}
	}
}

// TerminalExitCode gives the daemon time to publish metadata after output ends.
func TerminalExitCode(ctx context.Context, terminal Terminal) int {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for attempt := 0; attempt < 4; attempt++ {
		if code := terminal.ExitCode(ctx); code >= 0 {
			return code
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return -1
		case <-timer.C:
		}
	}
	return -1
}

package main

import (
	"bytes"
	"errors"
)

// Use a named field: embedding bytes.Buffer promotes ReadFrom, allowing
// io.Copy (including os/exec stdout pipes) to bypass the bounded Write method.
type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > (8<<20)-b.buffer.Len() {
		return 0, errors.New("Docker inventory exceeds 8 MiB")
	}
	return b.buffer.Write(p)
}

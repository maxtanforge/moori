package main

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestCommandOutputLimitThroughPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	done := make(chan struct{})
	go func() { defer close(done); defer writer.Close(); _, _ = writer.Write(bytes.Repeat([]byte("x"), 9<<20)) }()
	var output boundedOutput
	_, err = io.Copy(&output, reader)
	reader.Close()
	<-done
	if err == nil || len(output.Bytes()) > 8<<20 {
		t.Fatalf("pipe bypassed limit: bytes=%d err=%v", len(output.Bytes()), err)
	}
}

func TestCommandOutputAcceptsExactLimit(t *testing.T) {
	var output boundedOutput
	data := bytes.Repeat([]byte("x"), 8<<20)
	if n, err := output.Write(data); err != nil || n != len(data) {
		t.Fatalf("exact limit: %d %v", n, err)
	}
	if _, err := output.Write([]byte("x")); err == nil {
		t.Fatal("exceeded limit")
	}
}

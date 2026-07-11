package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"
)

func TestLogStreamRejectsFrameOverLimit(t *testing.T) {
	sub := "oversized"
	ch := subscribeLogs(sub)
	defer unsubscribeLogs(sub, ch)

	var frame bytes.Buffer
	header := make([]byte, 8)
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:], maxDockerLogFrame+1)
	frame.Write(header)

	globalLogTailer.stream(context.Background(), io.NopCloser(&frame), sub)

	select {
	case line := <-ch:
		t.Fatalf("published oversized frame: %#v", line)
	default:
	}
}

func TestLogStreamAcceptsFrameAtLimit(t *testing.T) {
	sub := "boundary"
	ch := subscribeLogs(sub)
	defer unsubscribeLogs(sub, ch)

	payload := bytes.Repeat([]byte("x"), maxDockerLogFrame)
	var frame bytes.Buffer
	header := make([]byte, 8)
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:], maxDockerLogFrame)
	frame.Write(header)
	frame.Write(payload)

	globalLogTailer.stream(context.Background(), io.NopCloser(&frame), sub)

	select {
	case line := <-ch:
		if len(line.Body) != maxDockerLogFrame {
			t.Fatalf("body length = %d, want %d", len(line.Body), maxDockerLogFrame)
		}
	default:
		t.Fatal("boundary frame was not published")
	}
}

package dl

import (
	"bytes"
	"context"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type testWriterAt struct {
	buf []byte
}

func (w *testWriterAt) WriteAt(p []byte, off int64) (int, error) {
	copy(w.buf[off:], p)
	return len(p), nil
}

func TestOffsetWriter(t *testing.T) {
	buf := make([]byte, 100)
	ow := &offsetWriter{
		w:      &testWriterAt{buf: buf},
		offset: 10,
	}

	data := []byte("hello world")
	n, err := ow.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to write %d bytes, wrote %d", len(data), n)
	}

	result := string(buf[10:21])
	if result != "hello world" {
		t.Errorf("expected 'hello world' at offset 10, got '%s'", result)
	}

	if ow.offset != 21 {
		t.Errorf("expected offset to be 21, got %d", ow.offset)
	}
}

func TestOffsetWriterMultipleWrites(t *testing.T) {
	buf := make([]byte, 100)
	ow := &offsetWriter{
		w:      &testWriterAt{buf: buf},
		offset: 0,
	}

	ow.Write([]byte("hello"))
	ow.Write([]byte(" "))
	ow.Write([]byte("world"))

	result := string(buf[:11])
	if result != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", result)
	}

	if ow.offset != 11 {
		t.Errorf("expected offset to be 11, got %d", ow.offset)
	}
}

func TestRateLimitedWriterNoLimiter(t *testing.T) {
	var buf bytes.Buffer
	rl := &rateLimitedWriter{
		w:       &buf,
		limiter: nil,
		ctx:     context.Background(),
	}

	data := []byte("test data")
	n, err := rl.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to write %d bytes, wrote %d", len(data), n)
	}
	if buf.String() != "test data" {
		t.Errorf("expected 'test data', got '%s'", buf.String())
	}
}

func TestRateLimitedWriterWithLimiter(t *testing.T) {
	var buf bytes.Buffer
	limiter := rate.NewLimiter(rate.Limit(1024*1024), 1024*1024) // 1MB/s
	rl := &rateLimitedWriter{
		w:       &buf,
		limiter: limiter,
		ctx:     context.Background(),
	}

	data := []byte("small test data")
	n, err := rl.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to write %d bytes, wrote %d", len(data), n)
	}
}

func TestRateLimitedWriterCancelledContext(t *testing.T) {
	var buf bytes.Buffer
	limiter := rate.NewLimiter(rate.Limit(100), 100) // Very slow
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rl := &rateLimitedWriter{
		w:       &buf,
		limiter: limiter,
		ctx:     ctx,
	}

	data := make([]byte, 1000)
	_, err := rl.Write(data)
	if err == nil {
		t.Error("expected error due to cancelled context")
	}
}

func TestRateLimitedWriterChunking(t *testing.T) {
	var buf bytes.Buffer
	limiter := rate.NewLimiter(rate.Limit(1024*1024), 1024*1024)
	rl := &rateLimitedWriter{
		w:       &buf,
		limiter: limiter,
		ctx:     context.Background(),
	}

	data := make([]byte, 50*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	n, err := rl.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to write %d bytes, wrote %d", len(data), n)
	}
	if buf.Len() != len(data) {
		t.Errorf("buffer has %d bytes, expected %d", buf.Len(), len(data))
	}
}

func TestRateLimitedWriterTimeout(t *testing.T) {
	var buf bytes.Buffer
	limiter := rate.NewLimiter(rate.Limit(10), 10) // 10 bytes/s
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	rl := &rateLimitedWriter{
		w:       &buf,
		limiter: limiter,
		ctx:     ctx,
	}

	data := make([]byte, 1000)
	_, err := rl.Write(data)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestWriterFunc(t *testing.T) {
	var called bool
	var receivedData []byte

	wf := WriterFunc(func(p []byte) (int, error) {
		called = true
		receivedData = make([]byte, len(p))
		copy(receivedData, p)
		return len(p), nil
	})

	data := []byte("test")
	n, err := wf.Write(data)

	if !called {
		t.Error("WriterFunc was not called")
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected %d, got %d", len(data), n)
	}
	if string(receivedData) != "test" {
		t.Errorf("expected 'test', got '%s'", string(receivedData))
	}
}

package main

import (
	"os"
	"testing"
)

func TestParseBandwidthLimit(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		hasError bool
	}{
		{"1M", 1024 * 1024, false},
		{"1MB", 1024 * 1024, false},
		{"500K", 500 * 1024, false},
		{"500KB", 500 * 1024, false},
		{"100KB/s", 100 * 1024, false},
		{"1.5M", int64(1.5 * 1024 * 1024), false},
		{"1G", 1024 * 1024 * 1024, false},
		{"", 0, false},
		{"invalid", 0, true},
		{"1X", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := parseBandwidthLimit(tt.input)
			if tt.hasError {
				if err == nil {
					t.Errorf("expected error for input %s, but got none", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for input %s: %v", tt.input, err)
				}
				if result != tt.expected {
					t.Errorf("for input %s, expected %d but got %d", tt.input, tt.expected, result)
				}
			}
		})
	}
}

func TestCalculatePartBoundary(t *testing.T) {
	dl := download{
		filesize: 1000,
		boost:    4,
	}

	tests := []struct {
		part      int
		expStart  uint64
		expEnd    uint64
	}{
		{0, 0, 249},
		{1, 250, 499},
		{2, 500, 749},
		{3, 750, 999}, // Last part gets remaining bytes
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.part)), func(t *testing.T) {
			start, end := dl.calculatePartBoundary(tt.part)
			if start != tt.expStart || end != tt.expEnd {
				t.Errorf("part %d: expected (%d, %d), got (%d, %d)",
					tt.part, tt.expStart, tt.expEnd, start, end)
			}
		})
	}
}

func TestFilenameFromURI(t *testing.T) {
	tests := []struct {
		uri      string
		expected string
	}{
		{"http://example.com/file.zip", "file.zip"},
		{"http://example.com/path/to/file.tar.gz", "file.tar.gz"},
		{"http://example.com/file.zip?query=param", "file.zip"},
		{"http://example.com/file%20with%20spaces.zip", "file with spaces.zip"},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			dl := download{uri: tt.uri}
			result := dl.filenameFromURI()
			if result != tt.expected {
				t.Errorf("for URI %s, expected %s but got %s", tt.uri, tt.expected, result)
			}
		})
	}
}

func TestVerifyChecksum(t *testing.T) {
	// Create a temporary file for testing
	content := []byte("test content")
	tmpfile, err := os.CreateTemp("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		checksum string
		hasError bool
	}{
		{"md5:9473fdd0d880a43c21b7778d34872157", false}, // Correct MD5 for "test content"
		{"sha256:6ae8a75555209fd6c44157c0aed8016e763ff435a19cf186f76863140143ff72", false}, // Correct SHA256
		{"md5:wronghash", true},
		{"sha256:wronghash", true},
		{"invalid:format", true},
		{"unsupported:hash", true},
	}

	for _, tt := range tests {
		t.Run(tt.checksum, func(t *testing.T) {
			err := verifyChecksum(tmpfile.Name(), tt.checksum)
			if tt.hasError {
				if err == nil {
					t.Errorf("expected error for checksum %s, but got none", tt.checksum)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for checksum %s: %v", tt.checksum, err)
				}
			}
		})
	}
}

func TestOffsetWriter(t *testing.T) {
	// Create a buffer to test writing
	buf := make([]byte, 100)
	
	// Create an offsetWriter starting at offset 10
	ow := &offsetWriter{
		w:      &testWriterAt{buf: buf},
		offset: 10,
	}
	
	// Write some data
	data := []byte("hello world")
	n, err := ow.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to write %d bytes, wrote %d", len(data), n)
	}
	
	// Check that data was written at the correct offset
	result := string(buf[10:21])
	if result != "hello world" {
		t.Errorf("expected 'hello world' at offset 10, got '%s'", result)
	}
	
	// Check that offset was updated
	if ow.offset != 21 {
		t.Errorf("expected offset to be 21, got %d", ow.offset)
	}
}

// testWriterAt implements io.WriterAt for testing
type testWriterAt struct {
	buf []byte
}

func (w *testWriterAt) WriteAt(p []byte, off int64) (int, error) {
	copy(w.buf[off:], p)
	return len(p), nil
}
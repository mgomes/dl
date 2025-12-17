package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCalculatePartBoundary(t *testing.T) {
	dl := download{
		filesize: 1000,
		boost:    4,
	}

	tests := []struct {
		part     int
		expStart uint64
		expEnd   uint64
	}{
		{0, 0, 249},
		{1, 250, 499},
		{2, 500, 749},
		{3, 750, 999},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("part_%d", tt.part), func(t *testing.T) {
			start, end := dl.calculatePartBoundary(tt.part)
			if start != tt.expStart || end != tt.expEnd {
				t.Errorf("part %d: expected (%d, %d), got (%d, %d)",
					tt.part, tt.expStart, tt.expEnd, start, end)
			}
		})
	}
}

func TestCalculatePartBoundaryUneven(t *testing.T) {
	dl := download{
		filesize: 1000,
		boost:    3,
	}

	start0, end0 := dl.calculatePartBoundary(0)
	start1, end1 := dl.calculatePartBoundary(1)
	start2, end2 := dl.calculatePartBoundary(2)

	if start0 != 0 || end0 != 332 {
		t.Errorf("part 0: expected (0, 332), got (%d, %d)", start0, end0)
	}
	if start1 != 333 || end1 != 665 {
		t.Errorf("part 1: expected (333, 665), got (%d, %d)", start1, end1)
	}
	if start2 != 666 || end2 != 999 {
		t.Errorf("part 2: expected (666, 999), got (%d, %d)", start2, end2)
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
		{"http://example.com/path/", ""},
		{"http://example.com/file.zip?a=1&b=2", "file.zip"},
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

func TestOutputPath(t *testing.T) {
	dl := download{
		workingDir: "/home/user/downloads",
		filename:   "test.zip",
	}

	expected := "/home/user/downloads/test.zip"
	result := dl.outputPath()
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestFetchMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			t.Errorf("expected HEAD request, got %s", r.Method)
		}
		w.Header().Set("Content-Length", "12345")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Disposition", `attachment; filename="test-file.zip"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dl := download{
		uri: server.URL + "/test",
		ctx: context.Background(),
	}

	if err := dl.FetchMetadata(); err != nil {
		t.Fatalf("FetchMetadata failed: %v", err)
	}

	if dl.filesize != 12345 {
		t.Errorf("expected filesize=12345, got %d", dl.filesize)
	}
	if !dl.supportsRange {
		t.Error("expected supportsRange=true")
	}
	if dl.filename != "test-file.zip" {
		t.Errorf("expected filename='test-file.zip', got '%s'", dl.filename)
	}
}

func TestFetchMetadataNoRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dl := download{
		uri: server.URL + "/file.zip",
		ctx: context.Background(),
	}

	if err := dl.FetchMetadata(); err != nil {
		t.Fatalf("FetchMetadata failed: %v", err)
	}

	if dl.supportsRange {
		t.Error("expected supportsRange=false when Accept-Ranges not set")
	}
	if dl.filename != "file.zip" {
		t.Errorf("expected filename='file.zip', got '%s'", dl.filename)
	}
}

func TestFetchMetadataNoContentLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dl := download{
		uri: server.URL + "/test",
		ctx: context.Background(),
	}

	err := dl.FetchMetadata()
	if err == nil {
		t.Error("expected error when Content-Length is missing")
	}
}

func TestFetchMetadataServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dl := download{
		uri: server.URL + "/test",
		ctx: context.Background(),
	}

	err := dl.FetchMetadata()
	if err == nil {
		t.Error("expected error on 404 response")
	}
}

func TestFetchSingleStream(t *testing.T) {
	content := []byte("hello world test content")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.Write(content)
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "dl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dl := download{
		uri:        server.URL + "/test.txt",
		ctx:        context.Background(),
		boost:      1,
		workingDir: tmpDir,
	}

	if err := dl.FetchMetadata(); err != nil {
		t.Fatalf("FetchMetadata failed: %v", err)
	}

	if err := dl.Fetch(); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	downloaded, err := os.ReadFile(dl.outputPath())
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}

	if string(downloaded) != string(content) {
		t.Errorf("content mismatch: expected '%s', got '%s'", content, downloaded)
	}
}

func TestFetchMultiPart(t *testing.T) {
	content := make([]byte, 10000)
	for i := range content {
		content[i] = byte(i % 256)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}

		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Write(content)
			return
		}

		var start, end int
		fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[start : end+1])
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "dl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dl := download{
		uri:        server.URL + "/test.bin",
		ctx:        context.Background(),
		boost:      4,
		retries:    3,
		workingDir: tmpDir,
	}

	if err := dl.FetchMetadata(); err != nil {
		t.Fatalf("FetchMetadata failed: %v", err)
	}

	if err := dl.Fetch(); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	downloaded, err := os.ReadFile(dl.outputPath())
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}

	if len(downloaded) != len(content) {
		t.Errorf("size mismatch: expected %d, got %d", len(content), len(downloaded))
	}

	for i := range content {
		if downloaded[i] != content[i] {
			t.Errorf("content mismatch at byte %d: expected %d, got %d", i, content[i], downloaded[i])
			break
		}
	}
}

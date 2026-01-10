package main

import (
	"os"
	"testing"
)

func TestVerifyChecksum(t *testing.T) {
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
		name     string
		checksum string
		hasError bool
	}{
		{"valid md5", "md5:9473fdd0d880a43c21b7778d34872157", false},
		{"valid sha256", "sha256:6ae8a75555209fd6c44157c0aed8016e763ff435a19cf186f76863140143ff72", false},
		{"wrong md5", "md5:wronghash", true},
		{"wrong sha256", "sha256:wronghash", true},
		{"missing colon", "invalidformat", true},
		{"unsupported algorithm", "sha512:abc123", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

func TestVerifyChecksumMissingFile(t *testing.T) {
	err := verifyChecksum("/nonexistent/file.txt", "md5:abc123")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestVerifyChecksumCaseInsensitive(t *testing.T) {
	content := []byte("test content")
	tmpfile, err := os.CreateTemp("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write(content); err != nil {
		t.Fatal(err)
	}
	tmpfile.Close()

	err = verifyChecksum(tmpfile.Name(), "MD5:9473FDD0D880A43C21B7778D34872157")
	if err != nil {
		t.Errorf("checksum should be case insensitive: %v", err)
	}

	err = verifyChecksum(tmpfile.Name(), "SHA256:6AE8A75555209FD6C44157C0AED8016E763FF435A19CF186F76863140143FF72")
	if err != nil {
		t.Errorf("checksum should be case insensitive: %v", err)
	}
}

func TestVerifyChecksumEmptyFile(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	tmpfile.Close()

	err = verifyChecksum(tmpfile.Name(), "md5:d41d8cd98f00b204e9800998ecf8427e")
	if err != nil {
		t.Errorf("should verify empty file md5: %v", err)
	}

	err = verifyChecksum(tmpfile.Name(), "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != nil {
		t.Errorf("should verify empty file sha256: %v", err)
	}
}

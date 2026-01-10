package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

func verifyChecksum(filepath string, checksumStr string) error {
	parts := strings.SplitN(checksumStr, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid checksum format, expected algorithm:hash")
	}

	algorithm := strings.ToLower(parts[0])
	expectedHash := strings.ToLower(parts[1])

	file, err := os.Open(filepath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var hasher hash.Hash
	switch algorithm {
	case "md5":
		hasher = md5.New()
	case "sha256":
		hasher = sha256.New()
	default:
		return fmt.Errorf("unsupported hash algorithm: %s (supported: md5, sha256)", algorithm)
	}

	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("failed to calculate hash: %w", err)
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))

	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

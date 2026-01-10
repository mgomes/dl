package dl

import (
	"fmt"
	"os"
	"runtime"
)

func supportsSparseFiles(path string) bool {
	switch runtime.GOOS {
	case "darwin":
		return true
	case "linux":
		return true
	case "windows":
		return false
	default:
		return false
	}
}

func createSparseFile(file *os.File, size int64) error {
	if _, err := file.Seek(size-1, 0); err != nil {
		return fmt.Errorf("failed to seek: %w", err)
	}
	if _, err := file.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to write sparse marker: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek to beginning: %w", err)
	}
	return nil
}

package main

import (
	"os"
	"runtime"
	"testing"
)

func TestSupportsSparseFiles(t *testing.T) {
	result := supportsSparseFiles("/tmp")

	switch runtime.GOOS {
	case "darwin", "linux":
		if !result {
			t.Errorf("expected sparse file support on %s", runtime.GOOS)
		}
	case "windows":
		if result {
			t.Error("expected no sparse file support on windows")
		}
	}
}

func TestCreateSparseFile(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "sparse-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	size := int64(1024 * 1024)
	if err := createSparseFile(tmpfile, size); err != nil {
		t.Fatalf("createSparseFile failed: %v", err)
	}

	stat, err := tmpfile.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if stat.Size() != size {
		t.Errorf("expected file size %d, got %d", size, stat.Size())
	}

	pos, err := tmpfile.Seek(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pos != 0 {
		t.Errorf("expected file position at 0, got %d", pos)
	}
}

func TestCreateSparseFileSmall(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "sparse-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	size := int64(100)
	if err := createSparseFile(tmpfile, size); err != nil {
		t.Fatalf("createSparseFile failed: %v", err)
	}

	stat, err := tmpfile.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if stat.Size() != size {
		t.Errorf("expected file size %d, got %d", size, stat.Size())
	}
}

func TestCreateSparseFileWritable(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "sparse-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	size := int64(1000)
	if err := createSparseFile(tmpfile, size); err != nil {
		t.Fatal(err)
	}

	data := []byte("test data")
	n, err := tmpfile.WriteAt(data, 500)
	if err != nil {
		t.Fatalf("WriteAt failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to write %d bytes, wrote %d", len(data), n)
	}

	buf := make([]byte, len(data))
	_, err = tmpfile.ReadAt(buf, 500)
	if err != nil {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if string(buf) != string(data) {
		t.Errorf("expected '%s', got '%s'", data, buf)
	}
}

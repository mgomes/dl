package dl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProgressFilePath(t *testing.T) {
	dl := Downloader{
		WorkingDir: "/tmp",
		Filename:   "test.zip",
	}

	expected := "/tmp/.test.zip.dl_progress"
	result := dl.progressFilePath()
	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}

func TestInitProgress(t *testing.T) {
	dl := Downloader{
		URI:      "http://example.com/file.zip",
		fileSize: 1000,
		Filename: "file.zip",
		Boost:    4,
		parts: []downloadPart{
			{index: 0, startByte: 0, endByte: 249},
			{index: 1, startByte: 250, endByte: 499},
			{index: 2, startByte: 500, endByte: 749},
			{index: 3, startByte: 750, endByte: 999},
		},
	}

	dl.initProgress()

	if dl.progress == nil {
		t.Fatal("progress should not be nil")
	}
	if dl.progress.URI != dl.URI {
		t.Errorf("expected URI %s, got %s", dl.URI, dl.progress.URI)
	}
	if dl.progress.FileSize != dl.fileSize {
		t.Errorf("expected filesize %d, got %d", dl.fileSize, dl.progress.FileSize)
	}
	if len(dl.progress.Parts) != 4 {
		t.Errorf("expected 4 parts, got %d", len(dl.progress.Parts))
	}

	for i, part := range dl.parts {
		pp := dl.progress.Parts[i]
		if pp.StartByte != part.startByte {
			t.Errorf("part %d: expected start %d, got %d", i, part.startByte, pp.StartByte)
		}
		if pp.EndByte != part.endByte {
			t.Errorf("part %d: expected end %d, got %d", i, part.endByte, pp.EndByte)
		}
		if pp.Completed {
			t.Errorf("part %d should not be completed", i)
		}
	}
}

func TestSaveAndLoadProgress(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dl := Downloader{
		URI:            "http://example.com/file.zip",
		fileSize:       1000,
		Filename:       "file.zip",
		WorkingDir:     tmpDir,
		Boost:          2,
		partDownloaded: make([]atomicCounter, 2),
		parts: []downloadPart{
			{index: 0, startByte: 0, endByte: 499},
			{index: 1, startByte: 500, endByte: 999},
		},
	}

	dl.initProgress()
	dl.partDownloaded[0].val = 250
	dl.partDownloaded[1].val = 100

	if err := dl.saveProgress(); err != nil {
		t.Fatalf("failed to save progress: %v", err)
	}

	progressPath := filepath.Join(tmpDir, ".file.zip.dl_progress")
	if _, err := os.Stat(progressPath); os.IsNotExist(err) {
		t.Error("progress file was not created")
	}

	dl2 := Downloader{
		URI:        "http://example.com/file.zip",
		fileSize:   1000,
		Filename:   "file.zip",
		WorkingDir: tmpDir,
	}

	if err := dl2.loadProgress(); err != nil {
		t.Fatalf("failed to load progress: %v", err)
	}

	if dl2.progress == nil {
		t.Fatal("progress should not be nil after load")
	}
	if dl2.progress.Parts[0].Downloaded != 250 {
		t.Errorf("expected part 0 downloaded=250, got %d", dl2.progress.Parts[0].Downloaded)
	}
	if dl2.progress.Parts[1].Downloaded != 100 {
		t.Errorf("expected part 1 downloaded=100, got %d", dl2.progress.Parts[1].Downloaded)
	}
}

func TestLoadProgressMismatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dl := Downloader{
		URI:            "http://example.com/file.zip",
		fileSize:       1000,
		Filename:       "file.zip",
		WorkingDir:     tmpDir,
		Boost:          1,
		partDownloaded: make([]atomicCounter, 1),
		parts: []downloadPart{
			{index: 0, startByte: 0, endByte: 999},
		},
	}
	dl.initProgress()
	dl.saveProgress()

	dl2 := Downloader{
		URI:        "http://example.com/different.zip",
		fileSize:   1000,
		Filename:   "file.zip",
		WorkingDir: tmpDir,
	}

	if err := dl2.loadProgress(); err != nil {
		t.Fatalf("loadProgress should not error: %v", err)
	}

	if dl2.progress != nil {
		t.Error("progress should be nil when URI doesn't match")
	}
}

func TestLoadProgressNoFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dl := Downloader{
		URI:        "http://example.com/file.zip",
		fileSize:   1000,
		Filename:   "file.zip",
		WorkingDir: tmpDir,
	}

	if err := dl.loadProgress(); err != nil {
		t.Fatalf("loadProgress should not error when file doesn't exist: %v", err)
	}

	if dl.progress != nil {
		t.Error("progress should be nil when no file exists")
	}
}

func TestRemoveProgress(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "dl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dl := Downloader{
		URI:            "http://example.com/file.zip",
		fileSize:       1000,
		Filename:       "file.zip",
		WorkingDir:     tmpDir,
		Boost:          1,
		partDownloaded: make([]atomicCounter, 1),
		parts: []downloadPart{
			{index: 0, startByte: 0, endByte: 999},
		},
	}
	dl.initProgress()
	dl.saveProgress()

	progressPath := dl.progressFilePath()
	if _, err := os.Stat(progressPath); os.IsNotExist(err) {
		t.Fatal("progress file should exist before removal")
	}

	if err := dl.removeProgress(); err != nil {
		t.Fatalf("failed to remove progress: %v", err)
	}

	if _, err := os.Stat(progressPath); !os.IsNotExist(err) {
		t.Error("progress file should not exist after removal")
	}
}

func TestUpdatePartProgress(t *testing.T) {
	dl := Downloader{
		URI:      "http://example.com/file.zip",
		fileSize: 1000,
		Filename: "file.zip",
		Boost:    2,
		parts: []downloadPart{
			{index: 0, startByte: 0, endByte: 499},
			{index: 1, startByte: 500, endByte: 999},
		},
	}
	dl.initProgress()

	dl.updatePartProgress(0, 300, false)
	if dl.progress.Parts[0].Downloaded != 300 {
		t.Errorf("expected downloaded=300, got %d", dl.progress.Parts[0].Downloaded)
	}
	if dl.progress.Parts[0].Completed {
		t.Error("part should not be completed")
	}

	dl.updatePartProgress(0, 500, true)
	if dl.progress.Parts[0].Downloaded != 500 {
		t.Errorf("expected downloaded=500, got %d", dl.progress.Parts[0].Downloaded)
	}
	if !dl.progress.Parts[0].Completed {
		t.Error("part should be completed")
	}
}

func TestGetTotalDownloaded(t *testing.T) {
	dl := Downloader{
		URI:      "http://example.com/file.zip",
		fileSize: 1000,
		Filename: "file.zip",
		Boost:    4,
		parts: []downloadPart{
			{index: 0, startByte: 0, endByte: 249},
			{index: 1, startByte: 250, endByte: 499},
			{index: 2, startByte: 500, endByte: 749},
			{index: 3, startByte: 750, endByte: 999},
		},
	}
	dl.initProgress()

	dl.progress.Parts[0].Downloaded = 100
	dl.progress.Parts[1].Downloaded = 200
	dl.progress.Parts[2].Downloaded = 150
	dl.progress.Parts[3].Downloaded = 50

	total := dl.getTotalDownloaded()
	if total != 500 {
		t.Errorf("expected total=500, got %d", total)
	}
}

func TestGetTotalDownloadedNilProgress(t *testing.T) {
	dl := Downloader{}
	total := dl.getTotalDownloaded()
	if total != 0 {
		t.Errorf("expected total=0 for nil progress, got %d", total)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

type DownloadProgress struct {
	Version     int                   `json:"version"`
	URI         string                `json:"uri"`
	FileSize    uint64                `json:"file_size"`
	Filename    string                `json:"filename"`
	Parts       map[int]*PartProgress `json:"parts"`
	Created     time.Time             `json:"created"`
	LastUpdated time.Time             `json:"last_updated"`
	Completed   bool                  `json:"completed"`
}

type PartProgress struct {
	Index        int       `json:"index"`
	StartByte    uint64    `json:"start_byte"`
	EndByte      uint64    `json:"end_byte"`
	Downloaded   uint64    `json:"downloaded"`
	Completed    bool      `json:"completed"`
	LastModified time.Time `json:"last_modified"`
}

func (dl *download) progressFilePath() string {
	return fmt.Sprintf("%s%c.%s.dl_progress", dl.workingDir, os.PathSeparator, dl.filename)
}

func (dl *download) loadProgress() error {
	progressPath := dl.progressFilePath()
	data, err := os.ReadFile(progressPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read progress file: %w", err)
	}

	var progress DownloadProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return fmt.Errorf("failed to parse progress file: %w", err)
	}

	if progress.URI != dl.uri || progress.FileSize != dl.filesize {
		fmt.Println("Progress file is for a different download, starting fresh")
		return nil
	}

	dl.progress = &progress
	return nil
}

func (dl *download) saveProgress() error {
	dl.progressMutex.Lock()
	defer dl.progressMutex.Unlock()

	if dl.progress == nil {
		return nil
	}

	for i := range dl.partDownloaded {
		if partProgress, ok := dl.progress.Parts[i]; ok {
			partProgress.Downloaded = atomic.LoadUint64(&dl.partDownloaded[i].val)
			partProgress.LastModified = time.Now()
		}
	}

	dl.progress.LastUpdated = time.Now()

	data, err := json.MarshalIndent(dl.progress, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal progress: %w", err)
	}

	tempPath := dl.progressFilePath() + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write progress file: %w", err)
	}

	if err := os.Rename(tempPath, dl.progressFilePath()); err != nil {
		return fmt.Errorf("failed to rename progress file: %w", err)
	}

	return nil
}

func (dl *download) removeProgress() error {
	return os.Remove(dl.progressFilePath())
}

func (dl *download) initProgress() {
	dl.progress = &DownloadProgress{
		Version:     1,
		URI:         dl.uri,
		FileSize:    dl.filesize,
		Filename:    dl.filename,
		Parts:       make(map[int]*PartProgress),
		Created:     time.Now(),
		LastUpdated: time.Now(),
	}

	for _, part := range dl.parts {
		dl.progress.Parts[part.index] = &PartProgress{
			Index:        part.index,
			StartByte:    part.startByte,
			EndByte:      part.endByte,
			Downloaded:   0,
			Completed:    false,
			LastModified: time.Now(),
		}
	}
}

func (dl *download) updatePartProgress(index int, downloaded uint64, completed bool) {
	dl.progressMutex.Lock()
	defer dl.progressMutex.Unlock()

	if dl.progress != nil && dl.progress.Parts[index] != nil {
		dl.progress.Parts[index].Downloaded = downloaded
		dl.progress.Parts[index].Completed = completed
		dl.progress.Parts[index].LastModified = time.Now()
	}
}

func (dl *download) getTotalDownloaded() uint64 {
	dl.progressMutex.Lock()
	defer dl.progressMutex.Unlock()

	if dl.progress == nil {
		return 0
	}

	var total uint64
	for _, part := range dl.progress.Parts {
		total += part.Downloaded
	}
	return total
}

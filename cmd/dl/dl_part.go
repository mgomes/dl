package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sync"

	"github.com/schollz/progressbar/v3"
)

type downloadPart struct {
	index     int
	uri       string
	dir       string
	filename  string
	startByte uint64
	endByte   uint64
}

func (p *downloadPart) downloadPartFilename() string {
	return path.Join(p.dir, fmt.Sprintf("download.part%d", p.index))
}

func (p *downloadPart) fetchPart(wg *sync.WaitGroup, bar *progressbar.ProgressBar, errCh chan<- error) {
	defer wg.Done()

	byteRange := fmt.Sprintf("bytes=%d-%d", p.startByte, p.endByte)
	req, err := http.NewRequest("GET", p.uri, nil)
	if err != nil {
		errCh <- fmt.Errorf("failed to create request for part %d: %w", p.index, err)
		return
	}
	req.Header.Set("Range", byteRange)
	req.Header.Set("User-Agent", "dl/1.0")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		errCh <- fmt.Errorf("failed to download part %d: %w", p.index, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errCh <- fmt.Errorf("non-2xx status (%d) for part %d", resp.StatusCode, p.index)
		return
	}

	// Create the file
	filename := p.downloadPartFilename()
	out, err := os.Create(filename)
	if err != nil {
		errCh <- fmt.Errorf("failed to create file for part %d: %w", p.index, err)
		return
	}
	defer out.Close()

	// Write the body to file
	if _, copyErr := io.Copy(io.MultiWriter(out, bar), resp.Body); copyErr != nil {
		errCh <- fmt.Errorf("error writing part %d to file: %w", p.index, copyErr)
	}
}

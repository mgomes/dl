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

func (p *downloadPart) fetchPart(wg *sync.WaitGroup, bar *progressbar.ProgressBar) {
	defer wg.Done()

	byteRange := fmt.Sprintf("bytes=%d-%d", p.startByte, p.endByte)
	req, _ := http.NewRequest("GET", p.uri, nil)
	req.Header.Set("Range", byteRange)
	req.Header.Set("User-Agent", "dl/1.0")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Create the file
	filename := p.downloadPartFilename()
	out, err := os.Create(filename)
	if err != nil {
		return
	}
	defer out.Close()

	// Write the body to file
	_, _ = io.Copy(io.MultiWriter(out, bar), resp.Body)
}

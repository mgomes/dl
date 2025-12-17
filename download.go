package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schollz/progressbar/v3"
	"golang.org/x/time/rate"
)

const (
	httpTimeout         = 30 * time.Second
	idleConnTimeout     = 90 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
	maxIdleConns        = 100
	maxIdleConnsPerHost = 10
	minDownloadTimeout  = 60 * time.Second
	timeoutPerMB        = 3 * time.Second
)

var httpClient = &http.Client{
	Timeout: httpTimeout,
	Transport: &http.Transport{
		MaxIdleConns:        maxIdleConns,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		ForceAttemptHTTP2:   false,
		DisableCompression:  true,
	},
}

type downloadPart struct {
	index     int
	uri       string
	startByte uint64
	endByte   uint64
}

type atomicCounter struct {
	val uint64
	_   [56]byte
}

type download struct {
	uri            string
	filesize       uint64
	filename       string
	workingDir     string
	boost          int
	retries        int
	resume         bool
	bandwidthLimit int64
	parts          []downloadPart
	supportsRange  bool
	ctx            context.Context
	progress       *DownloadProgress
	progressMutex  sync.Mutex
	partDownloaded []atomicCounter
}

func (dl *download) FetchMetadata() error {
	req, err := http.NewRequestWithContext(dl.ctx, "HEAD", dl.uri, nil)
	if err != nil {
		return fmt.Errorf("failed to create HEAD request: %w", err)
	}
	req.Header.Set("User-Agent", "dl/1.1.1")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HEAD request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status %d (%s)", resp.StatusCode, resp.Status)
	}

	contentLength := resp.Header.Get("Content-Length")
	if contentLength == "" {
		return fmt.Errorf("server did not provide Content-Length header, cannot determine file size")
	}

	dl.filesize, err = strconv.ParseUint(contentLength, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Content-Length '%s': %w", contentLength, err)
	}

	acceptRanges := resp.Header.Get("Accept-Ranges")
	dl.supportsRange = strings.ToLower(acceptRanges) == "bytes"

	contentDisposition := resp.Header.Get("Content-Disposition")
	if contentDisposition != "" {
		_, params, err := mime.ParseMediaType(contentDisposition)
		if err == nil && params["filename"] != "" {
			dl.filename = params["filename"]
		} else {
			dl.filename = dl.filenameFromURI()
		}
	} else {
		dl.filename = dl.filenameFromURI()
	}

	return nil
}

func (dl *download) Fetch() error {
	var outFile *os.File
	var err error
	var existingSize int64
	var resumeFromProgress bool

	if dl.boost > 1 && dl.supportsRange {
		dl.parts = make([]downloadPart, dl.boost)
		dl.partDownloaded = make([]atomicCounter, dl.boost)
		for i := 0; i < dl.boost; i++ {
			start, end := dl.calculatePartBoundary(i)
			dl.parts[i] = downloadPart{
				index:     i,
				uri:       dl.uri,
				startByte: start,
				endByte:   end,
			}
		}
	}

	if dl.resume {
		if err := dl.loadProgress(); err != nil {
			fmt.Printf("Warning: could not load progress file: %v\n", err)
		}

		if stat, err := os.Stat(dl.outputPath()); err == nil {
			existingSize = stat.Size()

			if dl.progress != nil && !dl.progress.Completed {
				resumeFromProgress = true
				totalDownloaded := dl.getTotalDownloaded()
				fmt.Printf("Resuming download using progress file (%.1f%% complete)\n",
					float64(totalDownloaded)/float64(dl.filesize)*100)
			} else if existingSize >= int64(dl.filesize) {
				fmt.Printf("File %s already fully downloaded (%d bytes)\n", dl.filename, existingSize)
				return nil
			} else if existingSize > 0 && dl.boost == 1 {
				fmt.Printf("Resuming download from %d bytes (%.1f%% complete)\n",
					existingSize, float64(existingSize)/float64(dl.filesize)*100)
			}

			outFile, err = os.OpenFile(dl.outputPath(), os.O_RDWR, 0644)
			if err != nil {
				return fmt.Errorf("cannot open file for resume: %w", err)
			}
		} else {
			if dl.progress != nil {
				fmt.Println("Progress file found but download file is missing, starting fresh")
				dl.progress = nil
				resumeFromProgress = false
			}
			dl.resume = false
		}
	}

	if outFile == nil {
		outFile, err = os.Create(dl.outputPath())
		if err != nil {
			return fmt.Errorf("cannot create output file: %w", err)
		}
	}
	defer outFile.Close()

	if dl.boost > 1 && dl.supportsRange && !dl.resume {
		if supportsSparseFiles(dl.workingDir) {
			if err := createSparseFile(outFile, int64(dl.filesize)); err != nil {
				fmt.Printf("Warning: sparse file creation failed, using regular allocation: %v\n", err)
				if err = outFile.Truncate(int64(dl.filesize)); err != nil {
					return fmt.Errorf("error setting file size: %w", err)
				}
			}
		} else {
			if err = outFile.Truncate(int64(dl.filesize)); err != nil {
				return fmt.Errorf("error setting file size: %w", err)
			}
		}
	}

	if dl.progress == nil && dl.boost > 1 && dl.supportsRange {
		dl.initProgress()
		if err := dl.saveProgress(); err != nil {
			fmt.Printf("Warning: could not save initial progress: %v\n", err)
		}
	}

	if resumeFromProgress && dl.progress != nil {
		for i := range dl.parts {
			if partProgress, ok := dl.progress.Parts[i]; ok {
				atomic.StoreUint64(&dl.partDownloaded[i].val, partProgress.Downloaded)
			}
		}
	}

	bar := progressbar.DefaultBytes(
		int64(dl.filesize),
		"Downloading",
	)

	if resumeFromProgress {
		bar.Set64(int64(dl.getTotalDownloaded()))
	} else if dl.resume && existingSize > 0 && dl.boost == 1 {
		bar.Set64(existingSize)
	}

	if dl.boost == 1 || !dl.supportsRange {
		return dl.fetchSingleStream(outFile, bar, existingSize)
	}

	return dl.fetchMultiPart(outFile, bar, resumeFromProgress)
}

func (dl *download) fetchSingleStream(outFile *os.File, bar *progressbar.ProgressBar, existingSize int64) error {
	downloadSize := dl.filesize
	if dl.resume && existingSize > 0 {
		downloadSize = dl.filesize - uint64(existingSize)
	}
	downloadSizeMB := downloadSize / (1024 * 1024)
	timeout := minDownloadTimeout + (time.Duration(downloadSizeMB) * timeoutPerMB)

	singleClient := &http.Client{
		Timeout:   timeout,
		Transport: httpClient.Transport,
	}

	req, err := http.NewRequestWithContext(dl.ctx, "GET", dl.uri, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "dl/1.1.1")

	startOffset := int64(0)
	if dl.resume && existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
		startOffset = existingSize
		bar.Set64(existingSize)
	}

	resp, err := singleClient.Do(req)
	if err != nil {
		return fmt.Errorf("single-stream download failed: %w", err)
	}
	defer resp.Body.Close()

	expectedStatus := http.StatusOK
	if startOffset > 0 {
		expectedStatus = http.StatusPartialContent
	}

	if resp.StatusCode != expectedStatus {
		return fmt.Errorf("server returned status %d (%s) for download", resp.StatusCode, resp.Status)
	}

	ow := &offsetWriter{
		w:      outFile,
		offset: startOffset,
	}

	var writer io.Writer = io.MultiWriter(ow, bar)
	if dl.bandwidthLimit > 0 {
		limiter := rate.NewLimiter(rate.Limit(dl.bandwidthLimit), int(dl.bandwidthLimit))
		writer = &rateLimitedWriter{w: writer, limiter: limiter, ctx: dl.ctx}
	}

	_, err = io.Copy(writer, resp.Body)
	if err != nil {
		return err
	}

	if dl.progress != nil {
		_ = dl.removeProgress()
	}
	return nil
}

func (dl *download) fetchMultiPart(outFile *os.File, bar *progressbar.ProgressBar, resumeFromProgress bool) error {
	var wg sync.WaitGroup
	errCh := make(chan error, dl.boost)
	progressCh := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(dl.ctx)
	defer cancel()

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := dl.saveProgress(); err != nil {
					fmt.Printf("Warning: could not save progress: %v\n", err)
				}
			case <-progressCh:
				if err := dl.saveProgress(); err != nil {
					fmt.Printf("Warning: could not save progress: %v\n", err)
				}
			}
		}
	}()

	go func() {
		uiTicker := time.NewTicker(100 * time.Millisecond)
		defer uiTicker.Stop()

		var lastTotal uint64
		for {
			select {
			case <-ctx.Done():
				var finalTotal uint64
				for i := range dl.partDownloaded {
					finalTotal += atomic.LoadUint64(&dl.partDownloaded[i].val)
				}
				if diff := finalTotal - lastTotal; diff > 0 {
					bar.Add64(int64(diff))
				}
				return
			case <-uiTicker.C:
				var currentTotal uint64
				for i := range dl.partDownloaded {
					currentTotal += atomic.LoadUint64(&dl.partDownloaded[i].val)
				}
				if diff := currentTotal - lastTotal; diff > 0 {
					bar.Add64(int64(diff))
					lastTotal = currentTotal
				}
			}
		}
	}()

	for _, part := range dl.parts {
		if resumeFromProgress && dl.progress.Parts[part.index] != nil && dl.progress.Parts[part.index].Completed {
			fmt.Printf("Part %d already completed, skipping\n", part.index)
			continue
		}

		wg.Add(1)
		go func(dp downloadPart) {
			defer wg.Done()
			if err := dl.fetchPartRange(dp, outFile); err != nil {
				errCh <- err
			}
		}(part)
	}

	wg.Wait()
	cancel()
	close(errCh)

	if dl.progress != nil {
		if err := dl.saveProgress(); err != nil {
			fmt.Printf("Warning: could not save final progress: %v\n", err)
		}
	}

	for e := range errCh {
		if e != nil {
			return e
		}
	}

	if dl.progress != nil {
		dl.progress.Completed = true
		if err := dl.saveProgress(); err != nil {
			fmt.Printf("Warning: could not save completion status: %v\n", err)
		}
		if err := dl.removeProgress(); err != nil {
			fmt.Printf("Warning: could not remove progress file: %v\n", err)
		}
	}

	return nil
}

func (dl *download) fetchPartRange(p downloadPart, outFile *os.File) error {
	var lastErr error
	baseDelay := 1 * time.Second

	for i := 0; i < dl.retries; i++ {
		err := dl.fetchPartOnce(p, outFile)
		if err == nil {
			return nil
		}
		lastErr = err

		if i < dl.retries-1 {
			delay := baseDelay * time.Duration(math.Pow(2, float64(i)))
			fmt.Printf("Part %d failed (attempt %d/%d): %v. Retrying in %v...\n",
				p.index, i+1, dl.retries, err, delay)
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("part %d failed after %d retries: %w", p.index, dl.retries, lastErr)
}

func (dl *download) fetchPartOnce(p downloadPart, outFile *os.File) error {
	startByte := p.startByte
	alreadyDownloaded := uint64(0)

	if dl.progress != nil && dl.progress.Parts[p.index] != nil {
		partProgress := dl.progress.Parts[p.index]
		if partProgress.Downloaded > 0 {
			alreadyDownloaded = partProgress.Downloaded
			startByte = p.startByte + alreadyDownloaded

			expectedSize := p.endByte - p.startByte + 1
			if alreadyDownloaded >= expectedSize {
				dl.updatePartProgress(p.index, alreadyDownloaded, true)
				return nil
			}
		}
	}

	partSize := p.endByte - startByte + 1
	partSizeMB := partSize / (1024 * 1024)
	timeout := minDownloadTimeout + (time.Duration(partSizeMB) * timeoutPerMB)

	partClient := &http.Client{
		Timeout:   timeout,
		Transport: httpClient.Transport,
	}

	byteRange := fmt.Sprintf("bytes=%d-%d", startByte, p.endByte)
	req, err := http.NewRequestWithContext(dl.ctx, "GET", p.uri, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Range", byteRange)
	req.Header.Set("User-Agent", "dl/1.1.1")

	resp, err := partClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download part %d: %w", p.index, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status %d (%s) for part %d (bytes %d-%d)",
			resp.StatusCode, resp.Status, p.index, startByte, p.endByte)
	}

	writeOffset := int64(startByte)
	partIndex := p.index

	trackerWriter := func(data []byte) (int, error) {
		n, err := outFile.WriteAt(data, writeOffset)
		if n > 0 {
			writeOffset += int64(n)
			atomic.AddUint64(&dl.partDownloaded[partIndex].val, uint64(n))
		}
		return n, err
	}

	var writer io.Writer = WriterFunc(trackerWriter)
	if dl.bandwidthLimit > 0 {
		perPartLimit := dl.bandwidthLimit / int64(dl.boost)
		if perPartLimit < 1024 {
			perPartLimit = 1024
		}
		limiter := rate.NewLimiter(rate.Limit(perPartLimit), int(perPartLimit))
		writer = &rateLimitedWriter{w: writer, limiter: limiter, ctx: dl.ctx}
	}

	buf := make([]byte, 1024*1024)
	if _, copyErr := io.CopyBuffer(writer, resp.Body, buf); copyErr != nil {
		return fmt.Errorf("error writing part %d: %w", p.index, copyErr)
	}

	dl.updatePartProgress(p.index, atomic.LoadUint64(&dl.partDownloaded[p.index].val), true)

	return nil
}

func (dl *download) calculatePartBoundary(part int) (uint64, uint64) {
	chunkSize := dl.filesize / uint64(dl.boost)
	startByte := uint64(part) * chunkSize
	var endByte uint64

	if part == dl.boost-1 {
		endByte = dl.filesize - 1
	} else {
		endByte = startByte + chunkSize - 1
	}
	return startByte, endByte
}

func (dl *download) filenameFromURI() string {
	splitURI := strings.Split(dl.uri, "/")
	filename := splitURI[len(splitURI)-1]
	if idx := strings.Index(filename, "?"); idx != -1 {
		filename = filename[:idx]
	}
	filename = strings.ReplaceAll(filename, "%20", " ")
	return filename
}

func (dl *download) outputPath() string {
	return fmt.Sprintf("%s%c%s", dl.workingDir, os.PathSeparator, dl.filename)
}

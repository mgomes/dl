package dl

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

type Downloader struct {
	URI            string
	Filename       string
	WorkingDir     string
	Boost          int
	Retries        int
	Resume         bool
	BandwidthLimit int64
	Progress       ProgressReporter
	Context        context.Context

	fileSize        uint64
	supportsRange   bool
	parts           []downloadPart
	progress        *DownloadProgress
	progressMutex   sync.Mutex
	partDownloaded  []atomicCounter
	metadataFetched bool
}

const (
	DefaultBoost   = 8
	DefaultRetries = 3
)

func (dl *Downloader) FileSize() uint64 {
	return dl.fileSize
}

func (dl *Downloader) SupportsRange() bool {
	return dl.supportsRange
}

func (dl *Downloader) FetchMetadata() error {
	if dl.URI == "" {
		return fmt.Errorf("uri is required")
	}

	if dl.Context == nil {
		dl.Context = context.Background()
	}

	req, err := http.NewRequestWithContext(dl.Context, "HEAD", dl.URI, nil)
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

	dl.fileSize, err = strconv.ParseUint(contentLength, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Content-Length '%s': %w", contentLength, err)
	}

	acceptRanges := resp.Header.Get("Accept-Ranges")
	dl.supportsRange = strings.ToLower(acceptRanges) == "bytes"

	if dl.Filename == "" {
		contentDisposition := resp.Header.Get("Content-Disposition")
		if contentDisposition != "" {
			_, params, err := mime.ParseMediaType(contentDisposition)
			if err == nil && params["filename"] != "" {
				dl.Filename = params["filename"]
			} else {
				dl.Filename = dl.filenameFromURI()
			}
		} else {
			dl.Filename = dl.filenameFromURI()
		}
	}

	dl.metadataFetched = true

	return nil
}

func (dl *Downloader) Fetch() error {
	var outFile *os.File
	var err error
	var existingSize int64
	var resumeFromProgress bool

	if err := dl.ensureDefaults(); err != nil {
		return err
	}

	if !dl.metadataFetched {
		if err := dl.FetchMetadata(); err != nil {
			return err
		}
	}

	reporter := dl.progressReporter()
	reporter.SetTotal(dl.fileSize)

	if dl.Boost > 1 && dl.supportsRange {
		dl.parts = make([]downloadPart, dl.Boost)
		dl.partDownloaded = make([]atomicCounter, dl.Boost)
		for i := range dl.Boost {
			start, end := dl.calculatePartBoundary(i)
			dl.parts[i] = downloadPart{
				index:     i,
				uri:       dl.URI,
				startByte: start,
				endByte:   end,
			}
		}
	}

	if dl.Resume {
		if err := dl.loadProgress(); err != nil {
			fmt.Printf("Warning: could not load progress file: %v\n", err)
		}

		if stat, err := os.Stat(dl.OutputPath()); err == nil {
			existingSize = stat.Size()

			if dl.progress != nil && !dl.progress.Completed {
				resumeFromProgress = true
				totalDownloaded := dl.getTotalDownloaded()
				fmt.Printf("Resuming download using progress file (%.1f%% complete)\n",
					float64(totalDownloaded)/float64(dl.fileSize)*100)
				reporter.SetDownloaded(totalDownloaded)
			} else if existingSize >= int64(dl.fileSize) {
				fmt.Printf("File %s already fully downloaded (%d bytes)\n", dl.Filename, existingSize)
				reporter.SetDownloaded(dl.fileSize)
				reporter.Done()
				return nil
			} else if existingSize > 0 && dl.Boost == 1 {
				fmt.Printf("Resuming download from %d bytes (%.1f%% complete)\n",
					existingSize, float64(existingSize)/float64(dl.fileSize)*100)
				reporter.SetDownloaded(uint64(existingSize))
			}

			outFile, err = os.OpenFile(dl.OutputPath(), os.O_RDWR, 0644)
			if err != nil {
				return fmt.Errorf("cannot open file for resume: %w", err)
			}
		} else {
			if dl.progress != nil {
				fmt.Println("Progress file found but download file is missing, starting fresh")
				dl.progress = nil
				resumeFromProgress = false
			}
			dl.Resume = false
		}
	}

	if outFile == nil {
		outFile, err = os.Create(dl.OutputPath())
		if err != nil {
			return fmt.Errorf("cannot create output file: %w", err)
		}
	}
	defer outFile.Close()

	if dl.Boost > 1 && dl.supportsRange && !dl.Resume {
		if supportsSparseFiles(dl.WorkingDir) {
			if err := createSparseFile(outFile, int64(dl.fileSize)); err != nil {
				fmt.Printf("Warning: sparse file creation failed, using regular allocation: %v\n", err)
				if err = outFile.Truncate(int64(dl.fileSize)); err != nil {
					return fmt.Errorf("error setting file size: %w", err)
				}
			}
		} else {
			if err = outFile.Truncate(int64(dl.fileSize)); err != nil {
				return fmt.Errorf("error setting file size: %w", err)
			}
		}
	}

	if dl.progress == nil && dl.Boost > 1 && dl.supportsRange {
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

	if dl.Boost == 1 || !dl.supportsRange {
		err = dl.fetchSingleStream(outFile, reporter, existingSize)
	} else {
		err = dl.fetchMultiPart(outFile, reporter, resumeFromProgress)
	}

	if err != nil {
		return err
	}

	reporter.Done()
	return nil
}

func (dl *Downloader) fetchSingleStream(outFile *os.File, reporter ProgressReporter, existingSize int64) error {
	downloadSize := dl.fileSize
	if dl.Resume && existingSize > 0 {
		downloadSize = dl.fileSize - uint64(existingSize)
	}
	downloadSizeMB := downloadSize / (1024 * 1024)
	timeout := minDownloadTimeout + (time.Duration(downloadSizeMB) * timeoutPerMB)

	singleClient := &http.Client{
		Timeout:   timeout,
		Transport: httpClient.Transport,
	}

	req, err := http.NewRequestWithContext(dl.Context, "GET", dl.URI, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "dl/1.1.1")

	startOffset := int64(0)
	if dl.Resume && existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
		startOffset = existingSize
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

	var writer io.Writer = io.MultiWriter(ow, &progressWriter{reporter: reporter})
	if dl.BandwidthLimit > 0 {
		limiter := rate.NewLimiter(rate.Limit(dl.BandwidthLimit), int(dl.BandwidthLimit))
		writer = &rateLimitedWriter{w: writer, limiter: limiter, ctx: dl.Context}
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

func (dl *Downloader) fetchMultiPart(outFile *os.File, reporter ProgressReporter, resumeFromProgress bool) error {
	var wg sync.WaitGroup
	errCh := make(chan error, dl.Boost)

	ctx, cancel := context.WithCancel(dl.Context)
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
			}
		}
	}()

	for _, part := range dl.parts {
		if resumeFromProgress && dl.progress.Parts[part.index] != nil && dl.progress.Parts[part.index].Completed {
			fmt.Printf("Part %d already completed, skipping\n", part.index)
			continue
		}

		part := part
		wg.Go(func() {
			if err := dl.fetchPartRange(part, outFile, reporter); err != nil {
				errCh <- err
			}
		})
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

func (dl *Downloader) fetchPartRange(p downloadPart, outFile *os.File, reporter ProgressReporter) error {
	var lastErr error
	baseDelay := 1 * time.Second

	for i := range dl.Retries {
		err := dl.fetchPartOnce(p, outFile, reporter)
		if err == nil {
			return nil
		}
		lastErr = err

		if i < dl.Retries-1 {
			delay := baseDelay * time.Duration(math.Pow(2, float64(i)))
			fmt.Printf("Part %d failed (attempt %d/%d): %v. Retrying in %v...\n",
				p.index, i+1, dl.Retries, err, delay)
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("part %d failed after %d retries: %w", p.index, dl.Retries, lastErr)
}

func (dl *Downloader) fetchPartOnce(p downloadPart, outFile *os.File, reporter ProgressReporter) error {
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
	req, err := http.NewRequestWithContext(dl.Context, "GET", p.uri, nil)
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
			reporter.AddDownloaded(uint64(n))
		}
		return n, err
	}

	var writer io.Writer = WriterFunc(trackerWriter)
	if dl.BandwidthLimit > 0 {
		perPartLimit := dl.BandwidthLimit / int64(dl.Boost)
		if perPartLimit < 1024 {
			perPartLimit = 1024
		}
		limiter := rate.NewLimiter(rate.Limit(perPartLimit), int(perPartLimit))
		writer = &rateLimitedWriter{w: writer, limiter: limiter, ctx: dl.Context}
	}

	buf := make([]byte, 1024*1024)
	if _, copyErr := io.CopyBuffer(writer, resp.Body, buf); copyErr != nil {
		return fmt.Errorf("error writing part %d: %w", p.index, copyErr)
	}

	dl.updatePartProgress(p.index, atomic.LoadUint64(&dl.partDownloaded[p.index].val), true)

	return nil
}

func (dl *Downloader) calculatePartBoundary(part int) (uint64, uint64) {
	chunkSize := dl.fileSize / uint64(dl.Boost)
	startByte := uint64(part) * chunkSize
	var endByte uint64

	if part == dl.Boost-1 {
		endByte = dl.fileSize - 1
	} else {
		endByte = startByte + chunkSize - 1
	}
	return startByte, endByte
}

func (dl *Downloader) filenameFromURI() string {
	splitURI := strings.Split(dl.URI, "/")
	filename := splitURI[len(splitURI)-1]
	if idx := strings.Index(filename, "?"); idx != -1 {
		filename = filename[:idx]
	}
	filename = strings.ReplaceAll(filename, "%20", " ")
	return filename
}

func (dl *Downloader) OutputPath() string {
	return fmt.Sprintf("%s%c%s", dl.WorkingDir, os.PathSeparator, dl.Filename)
}

func (dl *Downloader) ensureDefaults() error {
	if dl.URI == "" {
		return fmt.Errorf("uri is required")
	}

	if dl.Context == nil {
		dl.Context = context.Background()
	}

	if dl.WorkingDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}
		dl.WorkingDir = wd
	}

	if dl.Boost <= 0 {
		dl.Boost = DefaultBoost
	}

	if dl.Retries <= 0 {
		dl.Retries = DefaultRetries
	}

	return nil
}

func (dl *Downloader) progressReporter() ProgressReporter {
	if dl.Progress == nil {
		return noopProgressReporter{}
	}

	return dl.Progress
}

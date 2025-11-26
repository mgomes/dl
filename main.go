package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/schollz/progressbar/v3"
	"golang.org/x/time/rate"
)

// offsetWriter implements io.Writer by writing to an io.WriterAt
// at a specific offset, advancing offset after each Write call.
type offsetWriter struct {
	w      io.WriterAt
	offset int64
}

// Write writes len(p) bytes from p to the underlying data stream
// at offsetWriter.offset. Then offsetWriter.offset is incremented
// by the number of bytes written.
func (ow *offsetWriter) Write(p []byte) (int, error) {
	n, err := ow.w.WriteAt(p, ow.offset)
	ow.offset += int64(n)
	return n, err
}

// rateLimitedWriter wraps an io.Writer with rate limiting
type rateLimitedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
	ctx     context.Context
}

func (rl *rateLimitedWriter) Write(p []byte) (int, error) {
	if rl.limiter == nil {
		return rl.w.Write(p)
	}

	// Write in chunks to avoid blocking for too long
	written := 0
	for written < len(p) {
		chunkSize := 16 * 1024 // 16KB chunks
		if chunkSize > len(p)-written {
			chunkSize = len(p) - written
		}

		// Wait for permission to write
		if err := rl.limiter.WaitN(rl.ctx, chunkSize); err != nil {
			return written, err
		}

		n, err := rl.w.Write(p[written : written+chunkSize])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

type downloadPart struct {
	index     int
	uri       string
	startByte uint64
	endByte   uint64
}

// atomicCounter is padded to 64 bytes to prevent false sharing between CPU cores.
// Without padding, adjacent counters share a cache line, causing cache thrashing.
type atomicCounter struct {
	val uint64
	_   [56]byte // pad to 64 bytes (cache line size)
}

type download struct {
	uri            string
	filesize       uint64
	filename       string
	workingDir     string
	boost          int
	retries        int
	resume         bool
	bandwidthLimit int64 // bytes per second, 0 = unlimited
	parts          []downloadPart
	supportsRange  bool
	ctx            context.Context
	progress       *DownloadProgress
	progressMutex  sync.Mutex
	partDownloaded []atomicCounter // cache-line padded atomic counters for lock-free progress
}

// DownloadProgress tracks the progress of a download
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

// PartProgress tracks the progress of a single download part
type PartProgress struct {
	Index        int       `json:"index"`
	StartByte    uint64    `json:"start_byte"`
	EndByte      uint64    `json:"end_byte"`
	Downloaded   uint64    `json:"downloaded"`
	Completed    bool      `json:"completed"`
	LastModified time.Time `json:"last_modified"`
}

type config struct {
	boost   int
	retries int
}

const (
	// HTTP client timeouts
	httpTimeout         = 30 * time.Second // For HEAD requests and initial connections
	idleConnTimeout     = 90 * time.Second
	tlsHandshakeTimeout = 10 * time.Second

	// Connection pool settings
	maxIdleConns        = 100
	maxIdleConnsPerHost = 10

	// Download settings
	minDownloadTimeout = 60 * time.Second // Minimum timeout for downloads
	timeoutPerMB       = 3 * time.Second  // Additional timeout per MB of part size
)

// Global HTTP client with proper timeouts and connection pooling
var httpClient = &http.Client{
	Timeout: httpTimeout,
	Transport: &http.Transport{
		MaxIdleConns:        maxIdleConns,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		ForceAttemptHTTP2:   false, // Use multiple TCP connections for boosted downloads
		DisableCompression:  true,  // We're downloading files, compression is usually not helpful
	},
}

func loadConfig() config {
	cfg := config{
		boost:   8,
		retries: 3,
	}

	// Try to load from user's home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return cfg
	}

	configPath := filepath.Join(home, ".dlrc")
	file, err := os.Open(configPath)
	if err != nil {
		return cfg // File doesn't exist, use defaults
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "boost":
			if v, err := strconv.Atoi(value); err == nil && v > 0 {
				cfg.boost = v
			}
		case "retries":
			if v, err := strconv.Atoi(value); err == nil && v > 0 {
				cfg.retries = v
			}
		}
	}

	return cfg
}

// parseBandwidthLimit parses bandwidth limit strings like "1M", "500K", "100KB/s"
func parseBandwidthLimit(limit string) (int64, error) {
	if limit == "" {
		return 0, nil
	}

	// Remove "/s" suffix if present
	limit = strings.TrimSuffix(strings.ToUpper(limit), "/S")
	limit = strings.TrimSpace(limit)

	// Extract numeric part and unit
	var numStr string
	var unit string
	for i, ch := range limit {
		if ch >= '0' && ch <= '9' || ch == '.' {
			continue
		}
		numStr = limit[:i]
		unit = limit[i:]
		break
	}

	if numStr == "" {
		numStr = limit
	}

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid bandwidth limit: %s", limit)
	}

	// Convert to bytes per second
	multiplier := float64(1)
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "G", "GB":
		multiplier = 1024 * 1024 * 1024
	case "M", "MB":
		multiplier = 1024 * 1024
	case "K", "KB":
		multiplier = 1024
	case "B", "":
		multiplier = 1
	default:
		return 0, fmt.Errorf("unknown unit: %s", unit)
	}

	return int64(num * multiplier), nil
}

func main() {
	// Load config file first
	cfg := loadConfig()

	filenamePtr := flag.String("filename", "", "custom filename")
	boostPtr := flag.Int("boost", cfg.boost, "number of concurrent downloads")
	retriesPtr := flag.Int("retries", cfg.retries, "max retries for failed parts")
	resumePtr := flag.Bool("resume", true, "resume interrupted download (default: true)")
	noResumePtr := flag.Bool("no-resume", false, "disable auto-resume")
	limitPtr := flag.String("limit", "", "bandwidth limit (e.g. 1M, 500K, 100KB/s)")
	checksumPtr := flag.String("checksum", "", "verify download with checksum (format: algorithm:hash, e.g. sha256:abc123...)")

	flag.Parse()

	if *boostPtr < 1 {
		fmt.Fprintln(os.Stderr, "Boost must be greater than 0")
		os.Exit(1)
	}

	// Parse bandwidth limit
	bandwidthLimit, err := parseBandwidthLimit(*limitPtr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing bandwidth limit: %v\n", err)
		os.Exit(1)
	}

	fileURIs := flag.Args()
	if len(fileURIs) == 0 {
		fmt.Fprintln(os.Stderr, "No download URI(s) provided.")
		os.Exit(1)
	}

	// Create a context that can be cancelled
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-sigc
		fmt.Printf("\nReceived signal %s; cancelling downloads...\n", sig)
		cancel()
	}()

	for _, uri := range fileURIs {
		var dl download
		dl.uri = uri
		dl.boost = *boostPtr
		dl.retries = *retriesPtr
		dl.resume = *resumePtr && !*noResumePtr // Resume by default unless --no-resume is used
		dl.bandwidthLimit = bandwidthLimit
		dl.ctx = ctx

		// Fetch file metadata
		if err := dl.FetchMetadata(); err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching metadata for %s: %v\n", uri, err)
			os.Exit(1)
		}

		// Override filename if specified
		if *filenamePtr != "" {
			dl.filename = *filenamePtr
		}

		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
			os.Exit(1)
		}
		dl.workingDir = wd

		fmt.Println("Downloading:", dl.filename)

		// If the server does not support partial downloads and boost > 1, fallback to single download
		if !dl.supportsRange && dl.boost > 1 {
			fmt.Println("Server does not support partial content. Falling back to single-threaded download.")
			dl.boost = 1
		}

		// Perform the download
		if err := dl.Fetch(); err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Println("\nDownload cancelled")
				// Don't remove file on cancel - allow resume later
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error while downloading: %v\n", err)
			// Don't remove file on error if we have progress tracking
			if dl.progress == nil {
				_ = os.Remove(dl.outputPath())
			}
			os.Exit(1)
		}

		fmt.Println("Download completed:", dl.filename)

		// Verify checksum if provided
		if *checksumPtr != "" {
			fmt.Printf("Verifying checksum...")
			if err := verifyChecksum(dl.outputPath(), *checksumPtr); err != nil {
				fmt.Fprintf(os.Stderr, "\nChecksum verification failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(" ✓")
		}
	}
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

	// Check if server supports range requests
	acceptRanges := resp.Header.Get("Accept-Ranges")
	dl.supportsRange = strings.ToLower(acceptRanges) == "bytes"

	// Try to determine filename
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

// progressFilePath returns the path to the progress file
func (dl *download) progressFilePath() string {
	return fmt.Sprintf("%s%c.%s.dl_progress", dl.workingDir, os.PathSeparator, dl.filename)
}

// loadProgress loads existing progress from disk
func (dl *download) loadProgress() error {
	progressPath := dl.progressFilePath()
	data, err := os.ReadFile(progressPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No progress file yet
		}
		return fmt.Errorf("failed to read progress file: %w", err)
	}

	var progress DownloadProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return fmt.Errorf("failed to parse progress file: %w", err)
	}

	// Verify it's for the same download
	if progress.URI != dl.uri || progress.FileSize != dl.filesize {
		fmt.Println("Progress file is for a different download, starting fresh")
		return nil
	}

	dl.progress = &progress
	return nil
}

// saveProgress saves current progress to disk
func (dl *download) saveProgress() error {
	dl.progressMutex.Lock()
	defer dl.progressMutex.Unlock()

	if dl.progress == nil {
		return nil
	}

	// Sync from atomic counters to progress struct (lock-free reads)
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

	// Write to temp file first, then rename for atomicity
	tempPath := dl.progressFilePath() + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write progress file: %w", err)
	}

	if err := os.Rename(tempPath, dl.progressFilePath()); err != nil {
		return fmt.Errorf("failed to rename progress file: %w", err)
	}

	return nil
}

// removeProgress removes the progress file
func (dl *download) removeProgress() error {
	return os.Remove(dl.progressFilePath())
}

// initProgress initializes a new progress tracker
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

	// Initialize parts
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

// updatePartProgress updates the progress for a specific part
func (dl *download) updatePartProgress(index int, downloaded uint64, completed bool) {
	dl.progressMutex.Lock()
	defer dl.progressMutex.Unlock()

	if dl.progress != nil && dl.progress.Parts[index] != nil {
		dl.progress.Parts[index].Downloaded = downloaded
		dl.progress.Parts[index].Completed = completed
		dl.progress.Parts[index].LastModified = time.Now()
	}
}

// getTotalDownloaded returns the total bytes downloaded across all parts
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

// supportsSparseFiles checks if the filesystem supports sparse files
func supportsSparseFiles(path string) bool {
	// For now, we'll use a simple OS-based heuristic
	// In production, you might want to actually test by creating a sparse file
	switch runtime.GOOS {
	case "darwin":
		// macOS with APFS or HFS+ supports sparse files
		return true
	case "linux":
		// Most modern Linux filesystems support sparse files
		// Could enhance this by checking the actual filesystem type
		return true
	case "windows":
		// NTFS supports sparse files, but requires special handling
		return false
	default:
		return false
	}
}

// createSparseFile creates a sparse file of the given size
func createSparseFile(file *os.File, size int64) error {
	// On Unix-like systems, seeking past EOF and writing creates a sparse file
	if _, err := file.Seek(size-1, 0); err != nil {
		return fmt.Errorf("failed to seek: %w", err)
	}
	if _, err := file.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to write sparse marker: %w", err)
	}
	// Seek back to the beginning
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek to beginning: %w", err)
	}
	return nil
}

// Fetch downloads the file. If boost=1 or partial content not supported,
// it fetches in a single request. Otherwise, it launches multiple goroutines
// for parallel range requests, each writing to the correct position of the same file.
func (dl *download) Fetch() error {
	var outFile *os.File
	var err error
	var existingSize int64
	var resumeFromProgress bool

	// Prepare chunk boundaries first (needed for progress tracking)
	if dl.boost > 1 && dl.supportsRange {
		dl.parts = make([]downloadPart, dl.boost)
		dl.partDownloaded = make([]atomicCounter, dl.boost) // cache-line padded counters for lock-free progress
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

	// Check for existing progress
	if dl.resume {
		if err := dl.loadProgress(); err != nil {
			fmt.Printf("Warning: could not load progress file: %v\n", err)
		}

		// Check if file exists
		if stat, err := os.Stat(dl.outputPath()); err == nil {
			existingSize = stat.Size()

			// If we have valid progress metadata, use it
			if dl.progress != nil && !dl.progress.Completed {
				resumeFromProgress = true
				totalDownloaded := dl.getTotalDownloaded()
				fmt.Printf("Resuming download using progress file (%.1f%% complete)\n",
					float64(totalDownloaded)/float64(dl.filesize)*100)
			} else if existingSize >= int64(dl.filesize) {
				fmt.Printf("File %s already fully downloaded (%d bytes)\n", dl.filename, existingSize)
				return nil
			} else if existingSize > 0 && dl.boost == 1 {
				// For single-threaded downloads without progress file, resume from file size
				fmt.Printf("Resuming download from %d bytes (%.1f%% complete)\n",
					existingSize, float64(existingSize)/float64(dl.filesize)*100)
			}

			// Open for writing, don't truncate
			outFile, err = os.OpenFile(dl.outputPath(), os.O_RDWR, 0644)
			if err != nil {
				return fmt.Errorf("cannot open file for resume: %w", err)
			}
		} else {
			// File doesn't exist
			if dl.progress != nil {
				fmt.Println("Progress file found but download file is missing, starting fresh")
				dl.progress = nil
				resumeFromProgress = false
			}
			dl.resume = false
		}
	}

	// Create new file if needed
	if outFile == nil {
		outFile, err = os.Create(dl.outputPath())
		if err != nil {
			return fmt.Errorf("cannot create output file: %w", err)
		}
	}
	defer outFile.Close()

	// Set up the file size
	if dl.boost > 1 && dl.supportsRange && !dl.resume {
		// Use sparse file if supported
		if supportsSparseFiles(dl.workingDir) {
			if err := createSparseFile(outFile, int64(dl.filesize)); err != nil {
				// Fall back to truncate if sparse file creation fails
				fmt.Printf("Warning: sparse file creation failed, using regular allocation: %v\n", err)
				if err = outFile.Truncate(int64(dl.filesize)); err != nil {
					return fmt.Errorf("error setting file size: %w", err)
				}
			}
		} else {
			// Traditional pre-allocation
			if err = outFile.Truncate(int64(dl.filesize)); err != nil {
				return fmt.Errorf("error setting file size: %w", err)
			}
		}
	}

	// Initialize progress tracking if not resuming
	if dl.progress == nil && dl.boost > 1 && dl.supportsRange {
		dl.initProgress()
		if err := dl.saveProgress(); err != nil {
			fmt.Printf("Warning: could not save initial progress: %v\n", err)
		}
	}

	// Initialize atomic counters from existing progress (for resume)
	if resumeFromProgress && dl.progress != nil {
		for i := range dl.parts {
			if partProgress, ok := dl.progress.Parts[i]; ok {
				atomic.StoreUint64(&dl.partDownloaded[i].val, partProgress.Downloaded)
			}
		}
	}

	// Create a progress bar spanning the entire file
	bar := progressbar.DefaultBytes(
		int64(dl.filesize),
		"Downloading",
	)

	// Set initial progress if resuming
	if resumeFromProgress {
		bar.Set64(int64(dl.getTotalDownloaded()))
	} else if dl.resume && existingSize > 0 && dl.boost == 1 {
		bar.Set64(existingSize)
	}

	// Single-threaded download
	if dl.boost == 1 || !dl.supportsRange {
		// Calculate timeout for entire file
		downloadSize := dl.filesize
		if dl.resume && existingSize > 0 {
			downloadSize = dl.filesize - uint64(existingSize)
		}
		downloadSizeMB := downloadSize / (1024 * 1024)
		timeout := minDownloadTimeout + (time.Duration(downloadSizeMB) * timeoutPerMB)

		// Create a custom HTTP client with appropriate timeout
		singleClient := &http.Client{
			Timeout:   timeout,
			Transport: httpClient.Transport,
		}

		// Single-stream download
		req, err := http.NewRequestWithContext(dl.ctx, "GET", dl.uri, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("User-Agent", "dl/1.1.1")

		// For resume, request from where we left off
		startOffset := int64(0)
		if dl.resume && existingSize > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
			startOffset = existingSize
			bar.Set64(existingSize) // Update progress bar
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

		// Write to the appropriate offset in the file
		ow := &offsetWriter{
			w:      outFile,
			offset: startOffset,
		}

		// Create rate limiter if bandwidth limit is set
		var writer io.Writer = io.MultiWriter(ow, bar)
		if dl.bandwidthLimit > 0 {
			limiter := rate.NewLimiter(rate.Limit(dl.bandwidthLimit), int(dl.bandwidthLimit))
			writer = &rateLimitedWriter{w: writer, limiter: limiter, ctx: dl.ctx}
		}

		_, err = io.Copy(writer, resp.Body)
		if err != nil {
			return err
		}

		// Clean up progress file for single-threaded downloads
		if dl.progress != nil {
			_ = dl.removeProgress()
		}
		return nil
	}

	// Multi-part parallel download
	var wg sync.WaitGroup
	errCh := make(chan error, dl.boost)
	progressCh := make(chan struct{}, 1) // Channel to trigger progress saves

	// Start a goroutine to periodically save progress
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
				// Save immediately when requested
				if err := dl.saveProgress(); err != nil {
					fmt.Printf("Warning: could not save progress: %v\n", err)
				}
			}
		}
	}()

	// UI update ticker - updates progress bar without mutex contention in download loop
	go func() {
		uiTicker := time.NewTicker(100 * time.Millisecond)
		defer uiTicker.Stop()

		var lastTotal uint64
		for {
			select {
			case <-ctx.Done():
				// Final update to ensure bar reaches 100%
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

	// Launch goroutines
	for _, part := range dl.parts {
		// Skip completed parts when resuming
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

	// Wait until all parts complete
	wg.Wait()
	cancel() // Stop the progress saver
	close(errCh)

	// Final progress save
	if dl.progress != nil {
		if err := dl.saveProgress(); err != nil {
			fmt.Printf("Warning: could not save final progress: %v\n", err)
		}
	}

	// If any part failed, return the first error
	for e := range errCh {
		if e != nil {
			return e
		}
	}

	// Mark as completed and remove progress file
	if dl.progress != nil {
		dl.progress.Completed = true
		if err := dl.saveProgress(); err != nil {
			fmt.Printf("Warning: could not save completion status: %v\n", err)
		}
		// Remove progress file on successful completion
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
			// Exponential backoff: 1s, 2s, 4s
			delay := baseDelay * time.Duration(math.Pow(2, float64(i)))
			fmt.Printf("Part %d failed (attempt %d/%d): %v. Retrying in %v...\n",
				p.index, i+1, dl.retries, err, delay)
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("part %d failed after %d retries: %w", p.index, dl.retries, lastErr)
}

func (dl *download) fetchPartOnce(p downloadPart, outFile *os.File) error {
	// Check if we need to resume this part
	startByte := p.startByte
	alreadyDownloaded := uint64(0)

	if dl.progress != nil && dl.progress.Parts[p.index] != nil {
		partProgress := dl.progress.Parts[p.index]
		if partProgress.Downloaded > 0 {
			// Resume from where we left off
			alreadyDownloaded = partProgress.Downloaded
			startByte = p.startByte + alreadyDownloaded

			// If we've already downloaded everything for this part, mark as complete
			expectedSize := p.endByte - p.startByte + 1
			if alreadyDownloaded >= expectedSize {
				dl.updatePartProgress(p.index, alreadyDownloaded, true)
				return nil
			}
		}
	}

	// Calculate dynamic timeout based on part size
	partSize := p.endByte - startByte + 1
	partSizeMB := partSize / (1024 * 1024)
	timeout := minDownloadTimeout + (time.Duration(partSizeMB) * timeoutPerMB)

	// Create a custom HTTP client with appropriate timeout for this part
	partClient := &http.Client{
		Timeout:   timeout,
		Transport: httpClient.Transport, // Reuse the transport for connection pooling
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

	// Track write offset for this part
	writeOffset := int64(startByte)
	partIndex := p.index

	// Custom Write method that updates progress via atomic counter (no mutex, no locks)
	// Progress bar is updated by a separate ticker goroutine, not here
	trackerWriter := func(data []byte) (int, error) {
		n, err := outFile.WriteAt(data, writeOffset)
		if n > 0 {
			writeOffset += int64(n)
			atomic.AddUint64(&dl.partDownloaded[partIndex].val, uint64(n))
		}
		return n, err
	}

	// Create rate limiter if bandwidth limit is set
	var writer io.Writer = WriterFunc(trackerWriter)
	if dl.bandwidthLimit > 0 {
		// Divide bandwidth limit by number of concurrent parts
		perPartLimit := dl.bandwidthLimit / int64(dl.boost)
		if perPartLimit < 1024 { // Minimum 1KB/s per part
			perPartLimit = 1024
		}
		limiter := rate.NewLimiter(rate.Limit(perPartLimit), int(perPartLimit))
		writer = &rateLimitedWriter{w: writer, limiter: limiter, ctx: dl.ctx}
	}

	// Use 64KB buffer to reduce syscall overhead (default is 32KB)
	buf := make([]byte, 64*1024)
	if _, copyErr := io.CopyBuffer(writer, resp.Body, buf); copyErr != nil {
		return fmt.Errorf("error writing part %d: %w", p.index, copyErr)
	}

	// Mark part as completed (this one mutex call per part is fine)
	dl.updatePartProgress(p.index, atomic.LoadUint64(&dl.partDownloaded[p.index].val), true)

	return nil
}

// WriterFunc is an adapter to allow ordinary functions to be used as io.Writer
type WriterFunc func([]byte) (int, error)

func (f WriterFunc) Write(p []byte) (int, error) {
	return f(p)
}

// calculatePartBoundary calculates the start and end bytes for a part index.
func (dl *download) calculatePartBoundary(part int) (uint64, uint64) {
	chunkSize := dl.filesize / uint64(dl.boost)
	startByte := uint64(part) * chunkSize
	var endByte uint64

	// Last part gets any remaining bytes
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
	// Remove any query parameters
	if idx := strings.Index(filename, "?"); idx != -1 {
		filename = filename[:idx]
	}
	// Remove any URL encoding
	filename = strings.ReplaceAll(filename, "%20", " ")
	return filename
}

func (dl *download) outputPath() string {
	return fmt.Sprintf("%s%c%s", dl.workingDir, os.PathSeparator, dl.filename)
}

// verifyChecksum verifies the downloaded file against a provided checksum
// Format: algorithm:hash (e.g., "sha256:abc123...")
func verifyChecksum(filepath string, checksumStr string) error {
	parts := strings.SplitN(checksumStr, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid checksum format, expected algorithm:hash")
	}

	algorithm := strings.ToLower(parts[0])
	expectedHash := strings.ToLower(parts[1])

	// Open file
	file, err := os.Open(filepath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create hasher based on algorithm
	var hasher hash.Hash
	switch algorithm {
	case "md5":
		hasher = md5.New()
	case "sha256":
		hasher = sha256.New()
	default:
		return fmt.Errorf("unsupported hash algorithm: %s (supported: md5, sha256)", algorithm)
	}

	// Calculate hash
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("failed to calculate hash: %w", err)
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))

	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

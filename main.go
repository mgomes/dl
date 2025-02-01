package main

import (
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/schollz/progressbar/v3"
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

type downloadPart struct {
	index     int
	uri       string
	startByte uint64
	endByte   uint64
}

type download struct {
	uri           string
	filesize      uint64
	filename      string
	workingDir    string
	boost         int
	parts         []downloadPart
	supportsRange bool
}

func main() {
	filenamePtr := flag.String("filename", "", "custom filename")
	boostPtr := flag.Int("boost", 8, "number of concurrent downloads")
	workingDirPtr := flag.String("workdir", "", "working directory for downloads")

	flag.Parse()

	fileURIs := flag.Args()
	if len(fileURIs) == 0 {
		fmt.Fprintln(os.Stderr, "No URI provided.")
		os.Exit(1)
	}

	for _, uri := range fileURIs {
		var dl download
		dl.uri = uri
		dl.boost = *boostPtr

		// Fetch file metadata
		if err := dl.FetchMetadata(); err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching metadata: %v\n", err)
			os.Exit(1)
		}

		// Override filename if specified
		if *filenamePtr != "" {
			dl.filename = *filenamePtr
		}

		// Determine working directory
		if *workingDirPtr != "" {
			dl.workingDir = *workingDirPtr
		} else {
			wd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
				os.Exit(1)
			}
			dl.workingDir = wd
		}

		// Handle signals (to allow cleanup if needed)
		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
		go func() {
			sig := <-sigc
			fmt.Printf("\nReceived signal %s; aborting download...\n", sig)
			// If you want to remove the partially downloaded file on abort:
			_ = os.Remove(dl.outputPath())
			os.Exit(1)
		}()

		fmt.Println("Downloading:", dl.filename)

		// If the server does not support partial downloads and boost > 1, fallback to single download
		if !dl.supportsRange && dl.boost > 1 {
			fmt.Println("Server does not support partial content. Falling back to single-threaded download.")
			dl.boost = 1
		}

		// Perform the download
		if err := dl.Fetch(); err != nil {
			fmt.Fprintf(os.Stderr, "Error while downloading: %v\n", err)
			// Remove partially downloaded file upon error
			_ = os.Remove(dl.outputPath())
			os.Exit(1)
		}

		fmt.Println("Download completed:", dl.filename)
	}
}

func (dl *download) FetchMetadata() error {
	resp, err := http.Head(dl.uri)
	if err != nil {
		return fmt.Errorf("HEAD request failed: %w", err)
	}
	defer resp.Body.Close()

	contentLength := resp.Header.Get("Content-Length")
	if contentLength == "" {
		return fmt.Errorf("missing Content-Length header, cannot determine file size")
	}

	dl.filesize, err = strconv.ParseUint(contentLength, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Content-Length: %w", err)
	}

	// Check if server supports range requests
	acceptRanges := resp.Header.Get("Accept-Ranges")
	dl.supportsRange = strings.ToLower(acceptRanges) == "bytes"

	// Try to determine filename
	contentDisposition := resp.Header.Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		dl.filename = dl.filenameFromURI()
	} else {
		dl.filename = params["filename"]
		if dl.filename == "" {
			dl.filename = dl.filenameFromURI()
		}
	}

	return nil
}

// Fetch downloads the file. If boost=1 or partial content not supported,
// it fetches in a single request. Otherwise, it launches multiple goroutines
// for parallel range requests, each writing to the correct position of the same file.
func (dl *download) Fetch() error {
	// Create/Truncate the final file up front
	outFile, err := os.Create(dl.outputPath())
	if err != nil {
		return fmt.Errorf("cannot create output file: %w", err)
	}
	defer outFile.Close()

	// We set the file size right away (optional, but can be useful on some OSes)
	if dl.boost > 1 && dl.supportsRange {
		if err = outFile.Truncate(int64(dl.filesize)); err != nil {
			return fmt.Errorf("error setting file size: %w", err)
		}
	}

	// Create a progress bar spanning the entire file
	bar := progressbar.DefaultBytes(
		int64(dl.filesize),
		"Downloading",
	)

	if dl.boost == 1 || !dl.supportsRange {
		// Single-stream download
		req, err := http.NewRequest("GET", dl.uri, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("User-Agent", "dl/1.0")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("single-stream download failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("non-2xx status (%d) for single-stream", resp.StatusCode)
		}

		// Write everything to offset=0 in the final file
		ow := &offsetWriter{
			w:      outFile,
			offset: 0,
		}
		_, err = io.Copy(io.MultiWriter(ow, bar), resp.Body)
		return err
	}

	// Multi-part parallel download
	var wg sync.WaitGroup
	errCh := make(chan error, dl.boost)

	// Prepare chunk boundaries
	dl.parts = make([]downloadPart, dl.boost)
	for i := 0; i < dl.boost; i++ {
		start, end := dl.calculatePartBoundary(i)
		dl.parts[i] = downloadPart{
			index:     i,
			uri:       dl.uri,
			startByte: start,
			endByte:   end,
		}
	}

	// Launch goroutines
	for _, part := range dl.parts {
		wg.Add(1)
		go func(dp downloadPart) {
			defer wg.Done()
			if err := dl.fetchPartRange(dp, outFile, bar); err != nil {
				errCh <- err
			}
		}(part)
	}

	// Wait until all parts complete
	wg.Wait()
	close(errCh)

	// If any part failed, return the first error
	for e := range errCh {
		if e != nil {
			return e
		}
	}
	return nil
}

// fetchPartRange downloads the specific byte range for a part
// and writes it to the corresponding offset in outFile.
func (dl *download) fetchPartRange(p downloadPart, outFile *os.File, bar *progressbar.ProgressBar) error {
	// Construct the range header
	byteRange := fmt.Sprintf("bytes=%d-%d", p.startByte, p.endByte)
	req, err := http.NewRequest("GET", p.uri, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for part %d: %w", p.index, err)
	}
	req.Header.Set("Range", byteRange)
	req.Header.Set("User-Agent", "dl/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download part %d: %w", p.index, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("non-2xx status (%d) for part %d", resp.StatusCode, p.index)
	}

	// Write directly to the correct offset
	ow := &offsetWriter{
		w:      outFile,
		offset: int64(p.startByte),
	}
	if _, copyErr := io.Copy(io.MultiWriter(ow, bar), resp.Body); copyErr != nil {
		return fmt.Errorf("error writing part %d: %w", p.index, copyErr)
	}

	return nil
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
	return splitURI[len(splitURI)-1]
}

func (dl *download) outputPath() string {
	return fmt.Sprintf("%s%c%s", dl.workingDir, os.PathSeparator, dl.filename)
}

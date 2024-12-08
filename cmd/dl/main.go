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

		// Use filename from args if specified
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

		// Signal handling for cleanup
		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
		go func() {
			sig := <-sigc
			fmt.Printf("\nReceived signal %s; cleaning up...\n", sig)
			dl.cleanupParts()
			os.Exit(1)
		}()

		fmt.Println("Downloading:", dl.filename)

		// If the server does not support partial downloads and boost > 1, fallback to a single download stream
		if !dl.supportsRange && dl.boost > 1 {
			fmt.Println("Server does not support partial content. Falling back to single-threaded download.")
			dl.boost = 1
		}

		if err := dl.Fetch(); err != nil {
			fmt.Fprintf(os.Stderr, "Error while downloading parts: %v\n", err)
			dl.cleanupParts()
			os.Exit(1)
		}

		if err := dl.ConcatFiles(); err != nil {
			fmt.Fprintf(os.Stderr, "Error combining files: %v\n", err)
			dl.cleanupParts()
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

	dl.filesize, err = strconv.ParseUint(contentLength, 0, 64)
	if err != nil {
		return fmt.Errorf("invalid Content-Length: %w", err)
	}

	// Check if server supports range requests
	acceptRanges := resp.Header.Get("Accept-Ranges")
	dl.supportsRange = (strings.ToLower(acceptRanges) == "bytes")

	contentDisposition := resp.Header.Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		// If we fail to parse or filename not found, fallback to extracting from URI
		dl.filename = dl.filenameFromURI()
	} else {
		dl.filename = params["filename"]
		if dl.filename == "" {
			dl.filename = dl.filenameFromURI()
		}
	}

	return nil
}

func (dl *download) Fetch() error {
	var wg sync.WaitGroup

	errCh := make(chan error, dl.boost) // Collect errors from goroutines
	defer close(errCh)

	bar := progressbar.DefaultBytes(
		int64(dl.filesize),
		"Downloading",
	)

	// If boost == 1 or server does not support ranges, just download whole file at once
	if dl.boost == 1 {
		part := downloadPart{
			index:     0,
			uri:       dl.uri,
			dir:       dl.workingDir,
			startByte: 0,
			endByte:   dl.filesize - 1,
		}
		part.filename = part.downloadPartFilename()
		dl.parts = append(dl.parts, part)

		wg.Add(1)
		go part.fetchPart(&wg, bar, errCh)
		wg.Wait()

		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}

	// Multi-part download
	for i := 0; i < dl.boost; i++ {
		start, end := dl.calculatePartBoundary(i)
		wg.Add(1)
		dlPart := downloadPart{
			index:     i,
			uri:       dl.uri,
			dir:       dl.workingDir,
			startByte: start,
			endByte:   end,
		}
		dlPart.filename = dlPart.downloadPartFilename()
		dl.parts = append(dl.parts, dlPart)
		go dlPart.fetchPart(&wg, bar, errCh)
	}

	wg.Wait()

	// Check for errors
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func (dl *download) calculatePartBoundary(part int) (startByte uint64, endByte uint64) {
	chunkSize := dl.filesize / uint64(dl.boost)
	if part == 0 {
		startByte = 0
	} else {
		startByte = uint64(part) * chunkSize
	}

	// For the last part, pick up all remaining bytes
	if part == (dl.boost - 1) {
		endByte = dl.filesize - 1
	} else {
		endByte = startByte + chunkSize - 1
	}

	return
}

func (dl *download) filenameFromURI() string {
	splitURI := strings.Split(dl.uri, "/")
	return splitURI[len(splitURI)-1]
}

func (dl *download) ConcatFiles() error {
	// Verify that all parts exist
	for _, part := range dl.parts {
		if _, err := os.Stat(part.downloadPartFilename()); err != nil {
			return fmt.Errorf("missing part file: %s, error: %w", part.downloadPartFilename(), err)
		}
	}

	var readers []io.Reader

	bar := progressbar.DefaultBytes(
		int64(dl.filesize),
		"Combining  ",
	)

	for _, part := range dl.parts {
		downloadPart, err := os.Open(part.downloadPartFilename())
		if err != nil {
			return fmt.Errorf("error opening part file: %w", err)
		}
		defer downloadPart.Close()
		readers = append(readers, downloadPart)
	}

	inputFiles := io.MultiReader(readers...)

	outFile, err := os.Create(dl.filename)
	if err != nil {
		return fmt.Errorf("error creating output file: %w", err)
	}
	defer outFile.Close()

	_, err = io.Copy(io.MultiWriter(outFile, bar), inputFiles)
	if err != nil {
		return fmt.Errorf("error concatenating files: %w", err)
	}

	// Cleanup only after successful concatenation
	dl.cleanupParts()

	return nil
}

func (dl *download) cleanupParts() {
	for _, part := range dl.parts {
		os.Remove(part.downloadPartFilename())
	}
}

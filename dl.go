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
	uri        string
	filesize   uint64
	filename   string
	workingDir string
	boost      int
	parts      []downloadPart
}

func main() {
	filenamePtr := flag.String("filename", "", "custom filename")
	boostPtr := flag.Int("boost", 8, "number of concurrent downloads")
	workingDirPtr := flag.String("workdir", "", "working directory for downloads")

	flag.Parse()

	file_uris := flag.Args()

	var err error

	for _, uri := range file_uris {
		var dl download
		dl.uri = uri
		dl.boost = *boostPtr

		err = dl.FetchMetadata()
		if err != nil {
			panic(err)
		}

		// Use filename from args if specified
		if *filenamePtr != "" {
			dl.filename = *filenamePtr
		}

		if *workingDirPtr != "" {
			dl.workingDir = *workingDirPtr
		} else {
			dl.workingDir, err = os.Getwd()
			if err != nil {
				panic(err)
			}
		}

		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc,
			syscall.SIGHUP,
			syscall.SIGINT,
			syscall.SIGTERM,
			syscall.SIGQUIT)
		go func() {
			sig := <-sigc
			fmt.Printf("\n%s; cleaning up...\n", sig)
			dl.cleanupParts()
			os.Exit(0)
		}()

		fmt.Println(dl.filename)

		dl.Fetch()
		dl.ConcatFiles()
	}
}

func (dl *download) FetchMetadata() error {
	resp, err := http.Head(dl.uri)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	contentLength := resp.Header.Get("Content-Length")
	dl.filesize, err = strconv.ParseUint(contentLength, 0, 64)
	if err != nil {
		return err
	}

	contentDisposition := resp.Header.Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		dl.filename = dl.filenameFromURI()
		return err
	} else {
		dl.filename = params["filename"]
	}

	// No filename specified in the header; use the pathname
	if dl.filename == "" {
		dl.filename = dl.filenameFromURI()
	}

	return nil
}

func (dl *download) Fetch() error {
	var wg sync.WaitGroup

	bar := progressbar.DefaultBytes(
		int64(dl.filesize),
		"Downloading",
	)

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
		go dlPart.fetchPart(&wg, bar)
	}

	wg.Wait()
	return nil
}

func (dl *download) calculatePartBoundary(part int) (startByte uint64, endByte uint64) {
	chunkSize := dl.filesize / uint64(dl.boost)
	var previousEndByte uint64

	if part == 0 {
		startByte = 0
		previousEndByte = 0
	} else {
		previousEndByte = uint64(part)*chunkSize - 1
		startByte = previousEndByte + 1
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

func (dl *download) ConcatFiles() {
	var readers []io.Reader

	defer dl.cleanupParts()

	bar := progressbar.DefaultBytes(
		int64(dl.filesize),
		"Combining  ",
	)

	for _, part := range dl.parts {
		downloadPart, err := os.Open(part.downloadPartFilename())
		if err != nil {
			panic(err)
		}
		defer downloadPart.Close()
		readers = append(readers, downloadPart)
	}

	inputFiles := io.MultiReader(readers...)

	outFile, err := os.Create(dl.filename)
	if err != nil {
		panic(err)
	}

	_, err = io.Copy(io.MultiWriter(outFile, bar), inputFiles)
	if err != nil {
		panic(err)
	}
}

func (dl *download) cleanupParts() {
	for _, part := range dl.parts {
		os.Remove(part.downloadPartFilename())
	}
}

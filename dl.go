package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/schollz/progressbar/v3"
)

type download struct {
	uri      string
	filesize uint64
	filename string
}

type downloadPart struct {
	index     int
	uri       string
	dir       string
	startByte uint64
	endByte   uint64
}

func main() {
	filenamePtr := flag.String("filename", "", "custom filename")
	boostPtr := flag.Int("boost", 8, "number of concurrent downloads")
	workingDirPtr := flag.String("workdir", "", "working directory for downloads")

	flag.Parse()

	file_uris := flag.Args()

	var dl download
	var workingDir string
	var err error

	for _, uri := range file_uris {
		dl.uri = uri
		dl.filesize, dl.filename, err = fetchMetadata(dl.uri)
		if err != nil {
			panic(err)
		}

		// Use filename from args if specified
		if *filenamePtr != "" {
			dl.filename = *filenamePtr
		}

		if *workingDirPtr != "" {
			workingDir = *workingDirPtr
		} else {
			workingDir, err = os.Getwd()
			if err != nil {
				log.Println(err)
			}
		}

		fmt.Println(dl.filename)

		fetch(&dl, workingDir, *boostPtr)
		concatFiles(dl.filename, dl.filesize, *boostPtr, workingDir)
	}
}

func fetchMetadata(uri string) (filesize uint64, filename string, err error) {
	resp, err := http.Head(uri)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	contentLength := resp.Header.Get("Content-Length")
	filesize, err = strconv.ParseUint(contentLength, 0, 64)
	if err != nil {
		return
	}

	contentDisposition := resp.Header.Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		filename = filenameFromURI(uri)
		return filesize, filename, nil
	}
	filename = params["filename"]

	// No filename specified in the header; use the pathname
	if filename == "" {
		filename = filenameFromURI(uri)
	}

	return
}

func fetch(dl *download, dir string, boost int) {
	var wg sync.WaitGroup

	bar := progressbar.DefaultBytes(
		int64(dl.filesize),
		"Downloading",
	)

	for i := 0; i < boost; i++ {
		start, end := calculatePartBoundary(dl.filesize, boost, i)
		wg.Add(1)
		dlPart := downloadPart{
			index:     i,
			uri:       dl.uri,
			dir:       dir,
			startByte: start,
			endByte:   end,
		}
		go fetchPart(&wg, dlPart, bar)
	}

	wg.Wait()
}

func fetchPart(wg *sync.WaitGroup, part downloadPart, bar *progressbar.ProgressBar) {
	defer wg.Done()

	byteRange := fmt.Sprintf("bytes=%d-%d", part.startByte, part.endByte)
	req, _ := http.NewRequest("GET", part.uri, nil)
	req.Header.Set("Range", byteRange)
	req.Header.Set("User-Agent", "dl/1.0")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Create the file
	filename := downloadPartFilename(part.index, part.dir)
	out, err := os.Create(filename)
	if err != nil {
		return
	}
	defer out.Close()

	// Write the body to file
	_, _ = io.Copy(io.MultiWriter(out, bar), resp.Body)
}

func calculatePartBoundary(filesize uint64, totalParts int, part int) (startByte uint64, endByte uint64) {
	chunkSize := filesize / uint64(totalParts)
	var previousEndByte uint64

	if part == 0 {
		startByte = 0
		previousEndByte = 0
	} else {
		previousEndByte = uint64(part)*chunkSize - 1
		startByte = previousEndByte + 1
	}

	// For the last part, pick up all remaining bytes
	if part == (totalParts - 1) {
		endByte = filesize - 1
	} else {
		endByte = startByte + chunkSize - 1
	}

	return
}

func downloadPartFilename(index int, dir string) string {
	return path.Join(dir, fmt.Sprintf("download.part%d", index))
}

func filenameFromURI(uri string) string {
	splitURI := strings.Split(uri, "/")
	return splitURI[len(splitURI)-1]
}

func concatFiles(filename string, filesize uint64, parts int, dir string) {
	var readers []io.Reader

	bar := progressbar.DefaultBytes(
		int64(filesize),
		"Combining  ",
	)

	for i := 0; i < parts; i++ {
		downloadPart, err := os.Open(downloadPartFilename(i, dir))
		if err != nil {
			panic(err)
		}
		defer os.Remove(downloadPartFilename(i, dir))
		defer downloadPart.Close()
		readers = append(readers, downloadPart)
	}

	inputFiles := io.MultiReader(readers...)

	outFile, err := os.Create(filename)
	if err != nil {
		panic(err)
	}

	_, err = io.Copy(io.MultiWriter(outFile, bar), inputFiles)
	if err != nil {
		panic(err)
	}
}

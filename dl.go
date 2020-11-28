package main

import (
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

func main() {
	filenamePtr := flag.String("filename", "", "custom filename")
	boostPtr := flag.Int("boost", 8, "number of concurrent downloads")

	flag.Parse()

	uri := flag.Args()[0]

	var filesize uint64
	var filename string
	var err error

	filesize, filename, err = fetchMetadata(uri)
	if err != nil {
		panic(err)
	}

	// Use filename from args if specified
	if *filenamePtr != "" {
		filename = *filenamePtr
	}

	fmt.Println(filesize)
	fmt.Println(filename)

	fetch(uri, filesize, *boostPtr)
	concatFiles(filename, *boostPtr)

	return
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

func fetch(uri string, filesize uint64, boost int) {
	var wg sync.WaitGroup

	for part := 0; part < boost; part++ {
		start, end := calculatePartBoundary(filesize, boost, part)
		wg.Add(1)
		go fetchPart(&wg, part, uri, start, end)
	}

	wg.Wait()

	return
}

func fetchPart(wg *sync.WaitGroup, part int, uri string, startByte uint64, endByte uint64) {
	defer wg.Done()

	byteRange := fmt.Sprintf("bytes=%d-%d", startByte, endByte)
	req, _ := http.NewRequest("GET", uri, nil)
	req.Header.Set("Range", byteRange)
	req.Header.Set("User-Agent", "dl/1.0")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Create the file
	filename := downloadPartFilename(part)
	out, err := os.Create(filename)
	if err != nil {
		return
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)

	return
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

func downloadPartFilename(part int) string {
	return fmt.Sprintf("download.part%d", part)
}

func filenameFromURI(uri string) string {
	splitURI := strings.Split(uri, "/")
	return splitURI[len(splitURI)-1]
}

func concatFiles(filename string, parts int) {
	var readers []io.Reader

	for part := 0; part < parts; part++ {
		downloadPart, err := os.Open(downloadPartFilename(part))
		if err != nil {
			panic(err)
		}
		defer os.Remove(downloadPartFilename(part))
		defer downloadPart.Close()
		readers = append(readers, downloadPart)
	}

	inputFiles := io.MultiReader(readers...)

	outFile, err := os.Create(filename)
	if err != nil {
		panic(err)
	}

	_, err = io.Copy(outFile, inputFiles)
	if err != nil {
		panic(err)
	}

	return
}

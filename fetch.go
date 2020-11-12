package main

import (
    "flag"
    "fmt"
    "io"
    "net/http"
    "os"
    "strconv"
    "mime"
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
        return filesize, "", nil
    }
    filename = params["filename"]

    // No filename specified in the header; use the pathname
    if filename == "" {
        splitUri := strings.Split(uri, "/")
        filename = splitUri[len(splitUri)-1]
    }

    return
}

func fetch(uri string, filesize uint64, boost int) {
    var wg sync.WaitGroup

    for part := 0; part < boost; part++ {
        start, end := calculatePartBoundaries(filesize, boost, part)
        wg.Add(1)
        go fetchPart(&wg, part, uri, start, end)
    }

    wg.Wait()

    return
}

func fetchPart(wg *sync.WaitGroup, part int, uri string, start_byte uint64, end_byte uint64) {
    defer wg.Done()

    byte_range := fmt.Sprintf("bytes=%d-%d", start_byte, end_byte)
    req, _ := http.NewRequest("GET", uri, nil)
    req.Header.Set("Range", byte_range)
    req.Header.Set("User-Agent", "Fetch/1.0")

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

func calculatePartBoundaries(filesize uint64, total_parts int, part int) (start_byte uint64, end_byte uint64) {
    chunk_size := filesize / uint64(total_parts)
    var previous_end_byte uint64

    if part == 0 {
        start_byte = 0
        previous_end_byte = 0
    } else {
        // part is zero indexed so the multiplication is like using the previous part
        start_byte = uint64(part) * chunk_size
        previous_end_byte = start_byte - 1
    }

    // For the last part, pick up all remaining bytes
    if part == (total_parts - 1) {
      end_byte = filesize - 1
    } else {
      end_byte = previous_end_byte + chunk_size - 1
    }

    return
}

func downloadPartFilename(part int) string {
    return fmt.Sprintf("download.part%d", part)
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

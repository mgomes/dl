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
)

func main() {
    filenamePtr := flag.String("filename", "", "custom filename")
    // boostPtr := flag.Int("boost", 8, "number of concurrent downloads")

    flag.Parse()

    uri := flag.Args()[0]

    var filesize uint64
    var filename string
    var err error

    if *filenamePtr == "" {
        filesize, filename, err = FetchMetadata(uri)
        if err != nil {
            panic(err)
        }
    }

    fmt.Println(filesize)
    fmt.Println(filename)

    err = Fetch(filename, uri)
    if err != nil {
        panic(err)
    }

}

func FetchMetadata(uri string) (filesize uint64, filename string, err error) {
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
        return
    }
    filename = params["filename"]

    // No filename specified in the header; use the pathname
    if filename == "" {
        splitUri := strings.Split(uri, "/")
        filename = splitUri[len(splitUri)-1]
    }

    return
}

func Fetch(filepath string, uri string) (err error) {
    // Get the data
    resp, err := http.Get(uri)
    if err != nil {
        return
    }
    defer resp.Body.Close()

    // Create the file
    out, err := os.Create(filepath)
    if err != nil {
        return
    }
    defer out.Close()

    // Write the body to file
    _, err = io.Copy(out, resp.Body)
    return
}

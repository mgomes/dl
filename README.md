# dl

A download manager that uses multiple concurrent connections to speed up downloads.

## Features

- Concurrent downloading with configurable connection count
- Resumable downloads (enabled by default)
- Progress tracking that persists across restarts
- Bandwidth limiting
- Checksum verification (MD5, SHA256)
- Retry with exponential backoff

## Install

### Download

Grab the latest binary from the [releases page](https://github.com/mgomes/dl/releases).

### Build from source

Requires Go 1.23+

```bash
git clone https://github.com/mgomes/dl.git
cd dl
go build -o dl ./cmd/dl
```

## Usage

```bash
dl <url> [url2] [url3] ...
```

## Library usage

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mgomes/dl"
)

func main() {
	ctx := context.Background()
	downloader := dl.Downloader{
		URI:        "https://example.com/file.zip",
		Context:    ctx,
		WorkingDir: ".",
		Boost:      4,
		Retries:    3,
		Resume:     true,
	}

	if err := downloader.FetchMetadata(); err != nil {
		fmt.Printf("metadata error: %v\n", err)
		os.Exit(1)
	}

	if err := downloader.Fetch(); err != nil {
		fmt.Printf("download error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("saved to", downloader.OutputPath())
}
```

### Options

```
-filename string     Custom output filename
-boost int           Number of concurrent connections (default: 8)
-retries int         Max retries per part (default: 3)
-resume              Resume interrupted download (default: true)
-no-resume           Start fresh, ignore any existing progress
-limit string        Bandwidth limit (e.g. 1M, 500K, 100KB/s)
-checksum string     Verify file (format: algorithm:hash)
```

### Examples

```bash
# Basic download
dl https://example.com/file.zip

# Custom filename
dl -filename myfile.zip https://example.com/file.zip

# Resume happens automatically. If interrupted, just run again:
dl https://example.com/file.zip

# Force a fresh download
dl -no-resume https://example.com/file.zip

# Limit to 1 MB/s
dl -limit 1M https://example.com/file.zip

# Verify checksum after download
dl -checksum sha256:abc123... https://example.com/file.zip

# Use 4 connections with 5 retries
dl -boost 4 -retries 5 https://example.com/file.zip
```

## Configuration

You can set defaults in `~/.dlrc`:

```
boost = 8
retries = 3
```

## How it works

### Concurrent connections

The `-boost` flag controls how many parallel connections are used. The default of 8 works well for most cases. Going higher usually does not help since your connection will saturate.

### Resume

If a download is interrupted, just run the same command again. Progress is saved to a hidden `.filename.dl_progress` file every 2 seconds. Each part's byte position is tracked independently, so multi-connection downloads resume correctly.

The progress file is deleted after a successful download.

### Bandwidth limiting

The `-limit` flag accepts values like `1M`, `500K`, or `100KB/s`. When using multiple connections, the limit is split evenly between them.

### Checksum verification

```bash
dl -checksum sha256:e3b0c44... https://example.com/file.zip
dl -checksum md5:d41d8cd9... https://example.com/file.zip
```

### Retries

Failed parts retry with exponential backoff (1s, 2s, 4s). Use `-retries` to change the max attempts.

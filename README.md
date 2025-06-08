# dl

A fast, reliable download manager that utilizes concurrent connections to maximize download speeds.

## Features

- **Concurrent downloading** - Multiple connections for faster downloads
- **Auto-resume by default** - Automatically continues interrupted downloads
- **Progress persistence** - Tracks exact download state across restarts
- **Sparse file support** - Efficient disk usage on macOS/Linux
- **Bandwidth limiting** - Control download speed
- **Checksum verification** - Verify file integrity (MD5/SHA256)
- **Smart retry logic** - Exponential backoff for failed parts
- **Configuration file** - Save your preferred settings
- **Graceful cancellation** - Clean shutdown with Ctrl+C
- **Progress tracking** - Real-time progress with ETA

## Install

### Download (recommended)

Download the latest compiled release of `dl` from the [releases page](https://github.com/mgomes/dl/releases).

### Compiling from source

Requirements: Go 1.23+

```bash
git clone https://github.com/mgomes/dl.git
cd dl
go build
```

## Usage

### Basic Usage

```bash
dl <file url> [file2 url] [file3 url] ...
```

### Advanced Options

```bash
dl [options] <file url>

Options:
  -filename string     Custom filename for the download
  -boost int          Number of concurrent connections (default: 8)
  -retries int        Max retries for failed parts (default: 3)
  -resume             Resume interrupted download (default: true)
  -no-resume          Disable auto-resume functionality
  -limit string       Bandwidth limit (e.g. 1M, 500K, 100KB/s)
  -checksum string    Verify with checksum (format: algorithm:hash)
```

### Examples

```bash
# Basic download
dl https://example.com/file.zip

# Download with custom filename
dl -filename myfile.zip https://example.com/file.zip

# Downloads auto-resume by default (just re-run the same command)
dl https://example.com/file.zip

# Disable auto-resume for a fresh download
dl -no-resume https://example.com/file.zip

# Limit bandwidth to 1 MB/s
dl -limit 1M https://example.com/file.zip

# Verify download with SHA256
dl -checksum sha256:abc123... https://example.com/file.zip

# Use 4 connections with 5 retries
dl -boost 4 -retries 5 https://example.com/file.zip

# Combine multiple options
dl -boost 4 -limit 500K -filename data.tar.gz https://example.com/file.tar.gz
```

## Configuration File

Create a `.dlrc` file in your home directory to set default values:

```bash
# ~/.dlrc
boost = 8
retries = 3
```

## Advanced Features

### Concurrent Downloads

The `-boost` parameter controls how many simultaneous connections are used. Higher values aren't always better - your network throughput will eventually saturate. The default of 8 works well for most scenarios.

### Auto-Resume Capability

Downloads now automatically resume by default! If a download is interrupted (network failure, Ctrl+C, system crash), simply run the same command again:

```bash
# Start a download
dl https://example.com/large-file.zip
# ... download interrupted at 45% ...

# Just run the same command to resume
dl https://example.com/large-file.zip
# Output: Resuming download using progress file (45.0% complete)
```

Features:
- **Automatic detection** - Finds incomplete downloads and resumes automatically
- **Progress persistence** - Saves exact download state in `.filename.dl_progress` files
- **Multi-part awareness** - Resumes each parallel connection from exact byte position
- **Sparse file support** - Efficient disk usage on macOS/Linux filesystems
- **Smart validation** - Verifies same URL/filesize before resuming
- **Clean completion** - Removes progress files after successful download

Progress tracking details:
- Progress saved every 2 seconds during download
- Each download part tracked individually
- Survives crashes, network failures, and interruptions
- Works with multi-connection downloads (preserves boost setting)

To force a fresh download without resuming:
```bash
dl -no-resume https://example.com/file.zip
```

The tool shows clear status messages:
```
Resuming download using progress file (73.2% complete)
Part 0 already completed, skipping
Part 1 already completed, skipping
Downloading: 45.23 MB / 128.45 MB [=========>--------] 73.2% 3.2 MB/s 00:27
```

### Bandwidth Limiting

Control download speed with `-limit`:
- `1M` or `1MB` = 1 megabyte per second
- `500K` or `500KB` = 500 kilobytes per second  
- `100KB/s` = 100 kilobytes per second

When using multiple connections, the bandwidth is divided equally among all connections.

### Checksum Verification

Verify file integrity after download:
```bash
dl -checksum sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 https://example.com/file.zip
dl -checksum md5:d41d8cd98f00b204e9800998ecf8427e https://example.com/file.zip
```

Supported algorithms: `md5`, `sha256`

### Error Handling

`dl` includes robust error handling:
- **Exponential backoff** - Failed parts are retried with increasing delays (1s, 2s, 4s)
- **Configurable retries** - Set max retry attempts with `-retries`
- **Context-aware errors** - Clear error messages with relevant details
- **Graceful shutdown** - Ctrl+C cleanly cancels downloads

## Technical Details

- Uses HTTP/HTTPS with proper timeout configuration
- Connection pooling for efficient resource usage
- Progress tracking with real-time speed and ETA
- Smart chunk boundary calculation for optimal performance
- Rate limiting with context-aware cancellation

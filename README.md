# dl

Downloads files utilizing concurrent connections. For some ISPs, this can really increase download speeds.

## Install

### Download (recommended)

Download the latest compiled release of `dl` from the [releases page](https://github.com/mgomes/dl/releases).

### Compiling from source

After you've installed Go:

1. Clone this repo
2. Run `go build` in the root directory where you cloned the repo

## Usage

```
dl <file url> [file2 url] [file3 url] ...
```

### Custom Filename

By default, `dl` will use the file's HTTP metadata when available for the filename. If not available it will fallback to using the filename from the URI path.

To override this and set your own filename you can:

```
dl -filename blah.zip <file url>
```

### Boost

The boost will set the concurrency level. In typical concurrency scenarios you want to set this to the number of CPU threads available... however, we recommend keeping this at the default value of `8`. A higher value doesn't always lead to faster downloads. At some concurrency level, your network throughput will saturate.

```
dl -boost 8 <file url>
```

### Custom Working Directory

As of `dl` version 1.1, temporary files are no longer generated.

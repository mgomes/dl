# dl

Downloads files utilizing concurrent connections. For some ISPs, this can really increase download speeds.

Written as a proof of concept to compare against the Crystal version of the same utility: https://github.com/mgomes/grab

## Install

Since this is currently a WIP, a release version is not yet available. Installing `dl` currently requires compiling it from source.

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

The boost will set the concurrency level. In typical concurrency scenarios you want to set this to the number of CPU threads available... however, to be courteous the network host we recommend keeping this at the default value of `8`. A higher value doesn't always lead to faster downloads anyway. At some concurrency level, your network throughput will saturate

```
dl -boost 8 <file url>
```

### Custom Working Directory

By default, `dl` will use the current working directory to store the temporary downloaded parts. If you are downloading to an external disk, you may want to use an internal disk as the working directory. This will speed up the concatenation process after the download is complete.

To override this and set your own working directory you can:

```
dl -workdir ~/Somewhere/Else <file url>
```

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mgomes/dl"
)

func main() {
	cfg := loadConfig()

	filenamePtr := flag.String("filename", "", "custom filename")
	boostPtr := flag.Int("boost", cfg.boost, "number of concurrent downloads")
	retriesPtr := flag.Int("retries", cfg.retries, "max retries for failed parts")
	resumePtr := flag.Bool("resume", true, "resume interrupted download (default: true)")
	noResumePtr := flag.Bool("no-resume", false, "disable auto-resume")
	limitPtr := flag.String("limit", "", "bandwidth limit (e.g. 1M, 500K, 100KB/s)")
	checksumPtr := flag.String("checksum", "", "verify download with checksum (format: algorithm:hash, e.g. sha256:abc123...)")

	flag.Parse()

	if *boostPtr < 1 {
		fmt.Fprintln(os.Stderr, "Boost must be greater than 0")
		os.Exit(1)
	}

	bandwidthLimit, err := parseBandwidthLimit(*limitPtr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing bandwidth limit: %v\n", err)
		os.Exit(1)
	}

	fileURIs := flag.Args()
	if len(fileURIs) == 0 {
		fmt.Fprintln(os.Stderr, "No download URI(s) provided.")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-sigc
		fmt.Printf("\nReceived signal %s; cancelling downloads...\n", sig)
		cancel()
	}()

	for _, uri := range fileURIs {
		downloader := dl.Downloader{
			URI:            uri,
			Boost:          *boostPtr,
			Retries:        *retriesPtr,
			Resume:         *resumePtr && !*noResumePtr,
			BandwidthLimit: bandwidthLimit,
			Context:        ctx,
		}

		if err := downloader.FetchMetadata(); err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching metadata for %s: %v\n", uri, err)
			os.Exit(1)
		}

		if *filenamePtr != "" {
			downloader.Filename = *filenamePtr
		}

		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
			os.Exit(1)
		}
		downloader.WorkingDir = wd

		fmt.Println("Downloading:", downloader.Filename)

		if !downloader.SupportsRange() && downloader.Boost > 1 {
			fmt.Println("Server does not support partial content. Falling back to single-threaded download.")
			downloader.Boost = 1
		}

		downloader.Progress = newProgressBarReporter(downloader.FileSize())

		if err := downloader.Fetch(); err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Println("\nDownload cancelled")
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Error while downloading: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Download completed:", downloader.Filename)

		if *checksumPtr != "" {
			fmt.Printf("Verifying checksum...")
			if err := verifyChecksum(downloader.OutputPath(), *checksumPtr); err != nil {
				fmt.Fprintf(os.Stderr, "\nChecksum verification failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(" ✓")
		}
	}
}

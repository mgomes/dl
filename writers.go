package dl

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

type offsetWriter struct {
	w      io.WriterAt
	offset int64
}

func (ow *offsetWriter) Write(p []byte) (int, error) {
	n, err := ow.w.WriteAt(p, ow.offset)
	ow.offset += int64(n)
	return n, err
}

type rateLimitedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
	ctx     context.Context
}

func (rl *rateLimitedWriter) Write(p []byte) (int, error) {
	if rl.limiter == nil {
		return rl.w.Write(p)
	}

	written := 0
	for written < len(p) {
		chunkSize := 16 * 1024
		if chunkSize > len(p)-written {
			chunkSize = len(p) - written
		}

		if err := rl.limiter.WaitN(rl.ctx, chunkSize); err != nil {
			return written, err
		}

		n, err := rl.w.Write(p[written : written+chunkSize])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

type WriterFunc func([]byte) (int, error)

func (f WriterFunc) Write(p []byte) (int, error) {
	return f(p)
}

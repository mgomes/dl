package dl

type ProgressReporter interface {
	SetTotal(total uint64)
	SetDownloaded(downloaded uint64)
	AddDownloaded(delta uint64)
	Done()
}

type noopProgressReporter struct{}

func (noopProgressReporter) SetTotal(uint64)      {}
func (noopProgressReporter) SetDownloaded(uint64) {}
func (noopProgressReporter) AddDownloaded(uint64) {}
func (noopProgressReporter) Done()                {}

type progressWriter struct {
	reporter ProgressReporter
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	if pw.reporter != nil {
		pw.reporter.AddDownloaded(uint64(len(p)))
	}
	return len(p), nil
}

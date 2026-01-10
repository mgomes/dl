package main

import "github.com/schollz/progressbar/v3"

type progressBarReporter struct {
	bar *progressbar.ProgressBar
}

func newProgressBarReporter(total uint64) *progressBarReporter {
	return &progressBarReporter{
		bar: progressbar.DefaultBytes(int64(total), "Downloading"),
	}
}

func (p *progressBarReporter) SetTotal(total uint64) {
	if p.bar == nil {
		p.bar = progressbar.DefaultBytes(int64(total), "Downloading")
		return
	}
	p.bar.ChangeMax64(int64(total))
}

func (p *progressBarReporter) SetDownloaded(downloaded uint64) {
	if p.bar == nil {
		return
	}
	_ = p.bar.Set64(int64(downloaded))
}

func (p *progressBarReporter) AddDownloaded(delta uint64) {
	if p.bar == nil {
		return
	}
	_ = p.bar.Add64(int64(delta))
}

func (p *progressBarReporter) Done() {
	if p.bar == nil {
		return
	}
	_ = p.bar.Finish()
}

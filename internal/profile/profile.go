// Package profile provides opt-in phase timing for superschematic builds.
package profile

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Profiler records named phase durations for one schema service.
type Profiler struct {
	service string
	w       io.Writer
	mu      sync.Mutex
}

// New creates a profiler that writes stable machine-readable lines to w.
func New(service string, w io.Writer) *Profiler {
	if w == nil {
		return nil
	}
	return &Profiler{service: sanitize(service), w: w}
}

// Enabled reports whether the profiler will emit timing lines.
func (p *Profiler) Enabled() bool {
	return p != nil && p.w != nil
}

// Start starts timing phase and returns a function that records the elapsed
// duration. It is safe to call on a nil profiler.
func (p *Profiler) Start(phase string) func() {
	if !p.Enabled() {
		return func() {}
	}
	started := time.Now()
	return func() {
		p.Record(phase, time.Since(started))
	}
}

// Measure records the duration of fn under phase.
func (p *Profiler) Measure(phase string, fn func() error) error {
	done := p.Start(phase)
	err := fn()
	done()
	return err
}

// Record emits one timing line.
func (p *Profiler) Record(phase string, duration time.Duration) {
	if !p.Enabled() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	_, _ = fmt.Fprintf(
		p.w,
		"superschematic-profile service=%s phase=%s duration_ms=%d\n",
		p.service,
		sanitize(phase),
		duration.Milliseconds(),
	)
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return '_'
		}
	}, value)
}

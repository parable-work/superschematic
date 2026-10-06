package local

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

// console is where the provisioner writes: its own progress lines, and
// each process's output a line at a time, every line prefixed with the
// process's name, so the lines of two servers never interleave mid-line.
type console struct {
	mu  sync.Mutex
	out io.Writer
}

// printf writes one progress line.
func (c *console) printf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = fmt.Fprintf(c.out, format+"\n", args...)
}

// writer returns a writer that prefixes each line written to it with
// `[name] `. Flush writes a last line that has no newline.
func (c *console) writer(name string) *prefixWriter {
	return &prefixWriter{console: c, prefix: []byte("[" + name + "] ")}
}

// prefixWriter is one process stream's writer.
type prefixWriter struct {
	console *console
	prefix  []byte

	mu  sync.Mutex
	buf []byte
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i+1])
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush writes what is left of a line that never ended.
func (w *prefixWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(append(w.buf, '\n'))
		w.buf = nil
	}
}

func (w *prefixWriter) emit(line []byte) {
	w.console.mu.Lock()
	defer w.console.mu.Unlock()
	_, _ = w.console.out.Write(append(append([]byte(nil), w.prefix...), line...))
}

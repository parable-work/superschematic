package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// prompter asks for a secret at a terminal.
type prompter interface {
	// Secret shows prompt and returns the line typed, without its line
	// ending and without echoing it.
	Secret(prompt string) ([]byte, error)
}

// terminalPrompter returns the prompter over stdin, which writes its
// prompts to prompts, or nil when stdin is not a terminal.
func terminalPrompter(stdin io.Reader, prompts io.Writer) prompter {
	in, ok := stdin.(*os.File)
	if !ok || !isTerminal(in) {
		return nil
	}
	return &ttyPrompter{in: in, reader: bufio.NewReader(in), out: prompts}
}

// isTerminal reports whether f is a terminal: a character device stty can
// read the settings of, which /dev/null is not.
func isTerminal(f *os.File) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	cmd := exec.Command("stty", "-g")
	cmd.Stdin = f
	return cmd.Run() == nil
}

// ttyPrompter reads a line from the terminal with its echo turned off, as
// the compiler's stack commands do.
type ttyPrompter struct {
	in     *os.File
	reader *bufio.Reader
	out    io.Writer
}

func (p *ttyPrompter) Secret(prompt string) ([]byte, error) {
	if _, err := io.WriteString(p.out, prompt); err != nil {
		return nil, err
	}
	restore, err := hideInput(p.in)
	if err != nil {
		return nil, err
	}
	line, err := p.reader.ReadString('\n')
	restore()
	_, _ = io.WriteString(p.out, "\n")
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}

// hideInput turns the terminal's echo off with stty, and returns what
// turns it back on.
func hideInput(in *os.File) (restore func(), err error) {
	stty := func(arg string) error {
		cmd := exec.Command("stty", arg)
		cmd.Stdin = in
		return cmd.Run()
	}
	if err := stty("-echo"); err != nil {
		return nil, fmt.Errorf("turn the terminal's echo off: %w", err)
	}
	return func() { _ = stty("echo") }, nil
}
